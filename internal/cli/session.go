package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/plinkr/nautago/internal/nauta"
)

// shutdownTimeout es generoso porque una sesión a medio cerrar sigue facturando.
const shutdownTimeout = time.Minute

const tickInterval = time.Second

const (
	sessionTimeFlagHelp = "Tiempo de desconexión en segundos por defecto; admite 's', 'm' y 'h', por ejemplo: '90s', '10m' o '1h'"
	batchFlagHelp       = "Ejecutar en modo no interactivo"
	noLogFlagHelp       = "No guardar en la BD el log de esta conexión"
)

func (a *App) up(ctx context.Context, argv []string) error {
	cmd := a.newCommand("up", "nauta up [opciones] [usuario] [contraseña]",
		"Inicia una sesión Nauta y la mantiene abierta hasta que se interrumpa.")
	var (
		sessionTime string
		batch       bool
		noLog       bool
	)
	cmd.fs.StringVar(&sessionTime, "t", "", sessionTimeFlagHelp)
	cmd.fs.StringVar(&sessionTime, "session-time", "", sessionTimeFlagHelp)
	cmd.fs.BoolVar(&batch, "b", false, batchFlagHelp)
	cmd.fs.BoolVar(&batch, "batch", false, batchFlagHelp)
	cmd.fs.BoolVar(&noLog, "nl", false, noLogFlagHelp)
	cmd.fs.BoolVar(&noLog, "no-log", false, noLogFlagHelp)

	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if err := atMost(args, 2, "nauta up"); err != nil {
		return err
	}

	var user, password string
	if len(args) > 0 {
		user = args[0]
	}
	if len(args) > 1 {
		password = args[1]
	}

	name, secret, err := a.credentials(user, password)
	if err != nil {
		return err
	}
	limit, err := parseSessionTime(sessionTime)
	if err != nil {
		return err
	}

	client := a.newClient(name, secret)
	fmt.Fprintf(a.Out, "Conectando usuario: %s\n", name)

	if err := a.guardTunnelsBeforeLogin(ctx, client); err != nil {
		return err
	}
	if err := client.Login(ctx); err != nil {
		return err
	}

	if batch {
		// La sesión queda abierta a propósito; la cierra un `nauta down`.
		fmt.Fprintf(a.Out, "[Sesión iniciada: %s]\n", a.Now().Format("03:04:05 PM"))
		fmt.Fprintf(a.Out, "Tiempo restante: %s\n", a.remainingTime(ctx, client))
		return nil
	}

	return a.holdSession(ctx, client, limit, noLog)
}

func (a *App) holdSession(ctx context.Context, client *nauta.Client, limit time.Duration, noLog bool) error {
	// Ctrl+C tiene que cerrar la sesión, no matar el proceso antes del logout.
	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	if !noLog {
		if err := client.RecordLogin(); err != nil {
			// La sesión ya está abierta y factura: se cierra antes de reportar, y
			// si el cierre también falla el usuario tiene que enterarse.
			shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			return errors.Join(err, client.Logout(shutdown))
		}
	}

	started := a.Now()
	fmt.Fprintf(a.Out, "[Sesión iniciada: %s]\n", started.Format("03:04:05 PM"))
	fmt.Fprintf(a.Out, "Tiempo restante: %s\n", a.remainingTime(runCtx, client))
	fmt.Fprintf(a.Out, "Presione Ctrl+C para desconectarse, o ejecute 'nauta down' desde otro terminal\n")

	if err := a.reportProgress(runCtx, client, started, limit); err != nil {
		return err
	}

	// El logout no puede heredar la cancelación que acaba de terminar la sesión.
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	fmt.Fprint(a.Out, "\n\nCerrando sesión ...\n")
	fmt.Fprintf(a.Out, "Tiempo restante: %s\n", a.remainingTime(shutdown, client))
	if err := a.disconnect(shutdown, client); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Sesión cerrada con éxito: %s\n", a.Now().Format("03:04:05 PM"))
	return nil
}

// disconnect es el único camino de logout de la CLI: con un VPN arriba el portal
// no responde, y un comando que olvidara liberar el túnel dejaría la cuenta
// facturando.
func (a *App) disconnect(ctx context.Context, client *nauta.Client) error {
	a.releaseTunnels(ctx)

	// Otra terminal pudo haber desconectado ya.
	if !client.IsLoggedIn() {
		return nil
	}
	return client.Logout(ctx)
}

// guardTunnelsBeforeLogin existe porque un VPN se queda con la tabla de rutas y
// el sondeo de internet responde por el túnel, no por la red de Nauta.
func (a *App) guardTunnelsBeforeLogin(ctx context.Context, client *nauta.Client) error {
	if !a.tunnelRunning(ctx) {
		return nil
	}

	// El internet lo da el VPN y por ahí no se llega al portal. No se cierra el
	// túnel: cortarle la conexión a quien navega es peor que negarse.
	if client.IsConnected(ctx) {
		return tunnelHoldsTheConnection(client.IsLoggedIn())
	}

	// Sin internet, lo más probable es un túnel viejo que retiene las rutas.
	// Cerrarlo es lo único que devuelve la red de Nauta; Login vuelve a comprobar.
	a.releaseTunnels(ctx)
	return nil
}

// Envuelve los mismos errores que Login para que el llamador no distinga dos
// formas del mismo problema.
func tunnelHoldsTheConnection(sessionOpen bool) error {
	cause := nauta.ErrAlreadyConnected
	if sessionOpen {
		cause = nauta.ErrAlreadyLoggedIn
	}
	return fmt.Errorf("%w: el internet está llegando por el VPN, no por la red de Nauta. "+
		"Cierre el VPN para poder usar Nauta", cause)
}

func (a *App) tunnelRunning(ctx context.Context) bool {
	if a.Tunnels.OpenVPNRunning != nil && a.Tunnels.OpenVPNRunning() {
		return true
	}
	return a.Tunnels.ProtonVPNRunning != nil && a.Tunnels.ProtonVPNRunning(ctx)
}

func (a *App) reportProgress(ctx context.Context, client *nauta.Client, started time.Time, limit time.Duration) error {
	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()

	for {
		if !client.IsLoggedIn() {
			return nil
		}
		elapsed := a.Now().Sub(started)
		if limit > 0 && limit < elapsed {
			return nil
		}

		fmt.Fprintf(a.Out, "\rTiempo de conexión: %s", clock(elapsed))
		if limit > 0 {
			fmt.Fprintf(a.Out, " La sesión se cerrará en %s", clock(limit-elapsed))
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Las integraciones de VPN son opcionales: que falte la herramienta no es un
// fallo, así que los errores solo se ven con --debug.
func (a *App) releaseTunnels(ctx context.Context) {
	if a.Tunnels.OpenVPNRunning != nil && a.Tunnels.OpenVPNRunning() {
		fmt.Fprintln(a.Out, "Está ejecutando openvpn, voy a cerrarlo")
		if err := a.Tunnels.StopOpenVPN(ctx); err != nil {
			a.debugf("no se pudo cerrar openvpn: %v\n", err)
		}
	}
	if a.Tunnels.ProtonVPNRunning != nil && a.Tunnels.ProtonVPNRunning(ctx) {
		fmt.Fprintln(a.Out, "Está ejecutando protonvpn, voy a desconectarlo")
		if err := a.Tunnels.StopProtonVPN(ctx); err != nil {
			a.debugf("no se pudo desconectar protonvpn: %v\n", err)
		}
	}
}

func (a *App) down(ctx context.Context, argv []string) error {
	cmd := a.newCommand("down", "nauta down", "Cierra la sesión Nauta activa.")
	if _, err := cmd.parse(a, argv); err != nil {
		return err
	}

	client := a.newClient("", "")
	if !client.IsLoggedIn() {
		fmt.Fprintln(a.Out, "No hay ninguna sesión activa")
		return nil
	}
	if err := client.LoadSession(); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Desconectando usuario: %s\n", client.User())
	if err := a.disconnect(ctx, client); err != nil {
		return err
	}

	fmt.Fprintln(a.Out, "Sesión cerrada con éxito")
	return nil
}

func (a *App) isLoggedIn(argv []string) error {
	cmd := a.newCommand("is-logged-in", "nauta is-logged-in", "Indica si hay una sesión Nauta abierta.")
	if _, err := cmd.parse(a, argv); err != nil {
		return err
	}

	client := a.newClient("", "")
	fmt.Fprintf(a.Out, "Sesión activa: %s\n", yesNo(client.IsLoggedIn()))
	return nil
}

func (a *App) isOnline(ctx context.Context, argv []string) error {
	cmd := a.newCommand("is-online", "nauta is-online", "Indica si hay conexión a internet.")
	if _, err := cmd.parse(a, argv); err != nil {
		return err
	}

	client := a.newClient("", "")
	fmt.Fprintf(a.Out, "Online: %s\n", yesNo(client.IsConnected(ctx)))
	return nil
}

func (a *App) info(ctx context.Context, argv []string) error {
	cmd := a.newCommand("info", "nauta info [-h] [-c] [usuario] [contraseña]",
		"Muestra el usuario Nauta y el tiempo restante de su cuenta.")
	var credit bool
	cmd.fs.BoolVar(&credit, "c", false, "Muestra también el crédito de la cuenta")
	cmd.fs.BoolVar(&credit, "credit", false, "Muestra también el crédito de la cuenta")

	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if err := atMost(args, 2, "nauta info"); err != nil {
		return err
	}

	var user, password string
	if len(args) > 0 {
		user = args[0]
	}
	if len(args) > 1 {
		password = args[1]
	}

	name, secret, err := a.credentials(user, password)
	if err != nil {
		return err
	}

	client := a.newClient(name, secret)
	// Se reutilizan solo los tokens de la sesión abierta: la consulta es por el
	// usuario pedido, que puede no ser el que abrió la sesión.
	if client.IsLoggedIn() {
		if err := client.LoadSessionTokens(); err != nil {
			return err
		}
	}

	fmt.Fprintf(a.Out, "Usuario Nauta: %s\n", name)
	fmt.Fprintf(a.Out, "Tiempo restante: %s\n", a.remainingTime(ctx, client))

	if credit {
		// Con sesión abierta el portal no sirve el crédito; ese fallo es normal y
		// se muestra como texto en vez de abortar.
		fmt.Fprintf(a.Out, "Crédito: %s\n", a.userCredit(ctx, client))
	}
	return nil
}

// accountValue devuelve el mensaje de error en lugar del valor porque estas
// consultas son informativas y no deben abortar el comando. Libera los túneles
// aquí, y no en cada comando, porque con un VPN arriba el portal no responde.
func (a *App) accountValue(ctx context.Context, query func(context.Context) (string, error)) string {
	a.releaseTunnels(ctx)

	value, err := query(ctx)
	if err != nil {
		return err.Error()
	}
	return value
}

func (a *App) remainingTime(ctx context.Context, client *nauta.Client) string {
	return a.accountValue(ctx, client.RemainingTime)
}

func (a *App) userCredit(ctx context.Context, client *nauta.Client) string {
	return a.accountValue(ctx, client.UserCredit)
}

// Sin sufijo son segundos, como en NautaPy.
func parseSessionTime(value string) (time.Duration, error) {
	if value == "" {
		return 0, nil
	}

	lowered := strings.ToLower(value)
	multiplier := time.Second
	switch {
	case strings.HasSuffix(lowered, "h"):
		multiplier = time.Hour
		lowered = strings.TrimSuffix(lowered, "h")
	case strings.HasSuffix(lowered, "m"):
		multiplier = time.Minute
		lowered = strings.TrimSuffix(lowered, "m")
	case strings.HasSuffix(lowered, "s"):
		lowered = strings.TrimSuffix(lowered, "s")
	}

	amount, err := strconv.ParseInt(strings.TrimSpace(lowered), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("tiempo inválido: %q (use segundos, '90s', '30m' o '2h')", value)
	}
	// Un desbordamiento daría un límite negativo, que se lee como "sin límite".
	if amount < 0 || amount > math.MaxInt64/int64(multiplier) {
		return 0, fmt.Errorf("tiempo inválido: %q", value)
	}
	return time.Duration(amount) * multiplier, nil
}

func clock(d time.Duration) string {
	seconds := int64(d / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, (seconds%3600)/60, seconds%60)
}

func yesNo(value bool) string {
	if value {
		return "Sí"
	}
	return "No"
}

func atMost(args []string, limit int, name string) error {
	if len(args) > limit {
		return fmt.Errorf("%s acepta como máximo %d argumento(s)", name, limit)
	}
	return nil
}
