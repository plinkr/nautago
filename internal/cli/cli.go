// Package cli traduce la línea de comandos a operaciones de internal/nauta e
// internal/storage y presenta el resultado. Aquí no hay HTTP ni SQL.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/plinkr/nautago/internal/nauta"
	"github.com/plinkr/nautago/internal/platform"
	"github.com/plinkr/nautago/internal/storage"
)

// version la fija el release con
// -ldflags "-X github.com/plinkr/nautago/internal/cli.version=v0.9.0".
var version string

// buildVersion recurre, sin ldflags, a la versión del módulo que Go registra
// en el binario: el tag del commit o, si no lo tiene, una pseudo-versión.
func buildVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}

const sessionFileName = "nauta-session"

// errUsageRequested cuenta como fallo para que un `nauta` a secas no termine con
// código 0.
var errUsageRequested = errors.New("usage requested")

// Tunnels va detrás de funciones porque toca la tabla de procesos real, que debe
// quedarse fuera de las pruebas.
type Tunnels struct {
	OpenVPNRunning   func() bool
	StopOpenVPN      func(context.Context) error
	ProtonVPNRunning func(context.Context) bool
	StopProtonVPN    func(context.Context) error
}

func DefaultTunnels() Tunnels {
	return Tunnels{
		OpenVPNRunning:   func() bool { return platform.IsProcessRunning("openvpn") },
		StopOpenVPN:      platform.StopOpenVPN,
		ProtonVPNRunning: platform.IsProtonVPNRunning,
		StopProtonVPN:    platform.StopProtonVPN,
	}
}

type App struct {
	Out io.Writer
	Err io.Writer
	In  io.Reader

	Store *storage.Store
	Now   func() time.Time

	HTTPClient *http.Client
	RetryDelay time.Duration
	Tunnels    Tunnels

	debug bool
}

func New(store *storage.Store) *App {
	return &App{
		Out:     os.Stdout,
		Err:     os.Stderr,
		In:      os.Stdin,
		Store:   store,
		Now:     time.Now,
		Tunnels: DefaultTunnels(),
	}
}

func (a *App) Run(ctx context.Context, argv []string) int {
	err := a.run(ctx, argv)
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errUsageRequested):
		return 1
	default:
		var failure *commandFailure
		if errors.As(err, &failure) {
			if failure.err != nil {
				fmt.Fprintln(a.Err, err)
			}
			return failure.code
		}
		// Un fallo de red no es un rechazo del portal; el usuario necesita saber
		// cuál de los dos ocurrió para saber qué hacer.
		var netErr *nauta.NetworkError
		if errors.As(err, &netErr) {
			fmt.Fprintf(a.Err, "Hubo un problema en la red, por favor revise su conexión: %v\n", err)
			return 1
		}
		fmt.Fprintln(a.Err, err)
		return 1
	}
}

func (a *App) run(ctx context.Context, argv []string) error {
	global := flag.NewFlagSet("nauta", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	global.Usage = func() {}
	var (
		listConn, lastMonth, allConn, resumeConn bool
		showVersion                              bool
	)
	global.BoolVar(&a.debug, "d", false, "Muestra información de depuración")
	global.BoolVar(&a.debug, "debug", false, "Muestra información de depuración")
	global.BoolVar(&listConn, "lc", false, "Lista las conexiones del mes actual de los usuarios")
	global.BoolVar(&listConn, "list-conn", false, "Lista las conexiones del mes actual de los usuarios")
	global.BoolVar(&lastMonth, "lm", false, "Lista las conexiones del mes anterior")
	global.BoolVar(&lastMonth, "last-month", false, "Lista las conexiones del mes anterior")
	global.BoolVar(&allConn, "ac", false, "Lista todas las conexiones de los usuarios")
	global.BoolVar(&allConn, "all-conn", false, "Lista todas las conexiones de los usuarios")
	global.BoolVar(&resumeConn, "rc", false, "Hace un resumen mensual de todas las conexiones, por usuario")
	global.BoolVar(&resumeConn, "resume-conn", false, "Hace un resumen mensual de todas las conexiones, por usuario")
	global.BoolVar(&showVersion, "version", false, "Muestra la versión y termina")

	if err := global.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printUsage(global)
			return errUsageRequested
		}
		return err
	}
	if showVersion {
		fmt.Fprintf(a.Out, "nauta %s\n", buildVersion())
		return nil
	}

	command, rest := splitCommand(global.Args())

	if lastMonth && !listConn {
		return errors.New("--last-month requiere --list-conn")
	}
	if allConn && !listConn {
		return errors.New("--all-conn requiere --list-conn")
	}

	switch {
	case listConn:
		return a.listConnections(historyOptions{lastMonth: lastMonth, all: allConn, resume: resumeConn})
	case resumeConn:
		return a.resumeConnections()
	case command == "":
		a.printUsage(global)
		return errUsageRequested
	}
	return a.dispatch(ctx, command, rest)
}

func (a *App) dispatch(ctx context.Context, command string, args []string) error {
	switch command {
	case "up":
		return a.up(ctx, args)
	case "down":
		return a.down(ctx, args)
	case "info":
		return a.info(ctx, args)
	case "is-online":
		return a.isOnline(ctx, args)
	case "is-logged-in":
		return a.isLoggedIn(args)
	case "run-connected":
		return a.runConnected(ctx, args)
	case "users":
		return a.users(args)
	case "proxy":
		return a.proxy(ctx, args)
	default:
		return fmt.Errorf("subcomando desconocido: %s", command)
	}
}

// AdoptLegacySession trae la sesión que NautaPy dejó abierta para que
// `nauta down` pueda cerrarla; si no, el portal seguiría facturando sin que
// NautaGo la viera.
func (a *App) AdoptLegacySession(legacyDir string) (bool, error) {
	if legacyDir == "" {
		return false, nil
	}
	return nauta.AdoptSession(filepath.Join(legacyDir, sessionFileName), filepath.Join(a.Store.Dir(), sessionFileName))
}

func (a *App) newClient(user, password string) *nauta.Client {
	return nauta.NewClient(user, password, nauta.Config{
		SessionPath: filepath.Join(a.Store.Dir(), sessionFileName),
		HTTPClient:  a.HTTPClient,
		Log:         a.Store,
		Now:         a.Now,
		RetryDelay:  a.RetryDelay,
		// El historial no hace fallar al comando, pero tampoco se calla.
		OnHistoryError: func(err error) {
			fmt.Fprintf(a.Err, "Aviso: no se pudo guardar el log de la conexión: %v\n", err)
		},
	})
}

func (a *App) credentials(user, password string) (string, string, error) {
	name := user
	if name == "" {
		def, err := a.Store.DefaultUser()
		if err != nil {
			if errors.Is(err, storage.ErrNoUser) {
				return "", "", errors.New("No existe ningún usuario. Debe crear uno. Ejecute 'nauta --help' para más ayuda") //nolint:staticcheck // ST1005: mensaje visible heredado de NautaPy.
			}
			return "", "", err
		}
		name = def
	}
	return a.Store.FindCredentials(name, password)
}

func (a *App) debugf(format string, args ...any) {
	if a.debug {
		fmt.Fprintf(a.Err, format, args...)
	}
}
