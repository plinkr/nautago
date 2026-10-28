package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/plinkr/nautago/internal/nauta"
	"github.com/plinkr/nautago/internal/proxy"
)

const defaultProxyPort = 3128

const timeUnitFlagHelp = "Unidad mínima de división del tiempo según la tarifación (obligatorio); " +
	"en segundos por defecto, admite 's', 'm' y 'h', por ejemplo: '120' o '2m'"

func (a *App) proxy(ctx context.Context, argv []string) error {
	cmd := a.newCommand("proxy", "nauta proxy [-h] -t UNIDAD [-p PUERTO] [--host DIRECCIÓN] [-u USUARIO] [-m MÁX] [-l LOG] [--allow LISTA] [--deny LISTA]",
		"Proxy que abre una sesión Nauta al recibir peticiones y la cierra cuando no quedan conexiones.")
	var (
		host    string
		port    int
		unitArg string
		user    string
		maxConn int
		logPath string
		allow   string
		deny    string
	)
	cmd.fs.StringVar(&host, "host", "127.0.0.1", "Dirección donde escucha el proxy; use \"0.0.0.0\" para exponerlo a la red")
	cmd.fs.IntVar(&port, "p", defaultProxyPort, "Puerto del servicio")
	cmd.fs.IntVar(&port, "port", defaultProxyPort, "Puerto del servicio")
	cmd.fs.StringVar(&unitArg, "t", "", timeUnitFlagHelp)
	cmd.fs.StringVar(&unitArg, "time-unit", "", timeUnitFlagHelp)
	cmd.fs.StringVar(&user, "u", "", "Usuario Nauta con el que conectarse; por defecto, el usuario predeterminado")
	cmd.fs.StringVar(&user, "user", "", "Usuario Nauta con el que conectarse; por defecto, el usuario predeterminado")
	cmd.fs.IntVar(&maxConn, "m", 0, "Máximo número de conexiones simultáneas")
	cmd.fs.IntVar(&maxConn, "max-conn", 0, "Máximo número de conexiones simultáneas")
	cmd.fs.StringVar(&logPath, "l", "-", "Archivo de logs; use \"-\" para la salida estándar")
	cmd.fs.StringVar(&logPath, "log", "-", "Archivo de logs; use \"-\" para la salida estándar")
	cmd.fs.StringVar(&allow, "allow", "", "Dominios permitidos, separados por comas")
	cmd.fs.StringVar(&deny, "deny", "", "Dominios bloqueados, separados por comas")

	if _, err := cmd.parse(a, argv); err != nil {
		return err
	}
	unit, err := parseSessionTime(unitArg)
	if err != nil {
		return err
	}
	if unit <= 0 {
		return fmt.Errorf("nauta proxy: -t/--time-unit es obligatorio (por ejemplo -t 120s)")
	}

	name, password, err := a.credentials(user, "")
	if err != nil {
		return err
	}

	logs, closeLogs, err := a.openProxyLog(logPath)
	if err != nil {
		return err
	}
	defer closeLogs()

	server, err := proxy.New(proxy.Options{
		Host:     host,
		Port:     port,
		TimeUnit: unit,
		MaxConn:  maxConn,
		Log:      logs,
		Allow:    splitList(allow),
		Deny:     splitList(deny),
		Session:  a.newProxySession(name, password),
	})
	if err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Proxy Nauta escuchando en %s:%d (Ctrl+C para salir)\n", host, port)
	if err := server.Serve(ctx); err != nil {
		return err
	}
	return nil
}

func (a *App) newProxySession(user, password string) proxy.Session {
	return &portalSession{app: a, client: a.newClient(user, password)}
}

type portalSession struct {
	app    *App
	client *nauta.Client
}

func (s *portalSession) Exists() bool { return s.client.IsLoggedIn() }

// Open registra el login enseguida; si no, el cierre del proxy pondría la hora de
// fin sobre una fila vieja ajena.
//
// Un fallo del historial no hace fallar a Open: la sesión ya factura, y si el
// proxy no la diera por suya nunca la cerraría.
func (s *portalSession) Open(ctx context.Context) error {
	if err := s.app.guardTunnelsBeforeLogin(ctx, s.client); err != nil {
		return err
	}
	if err := s.client.Login(ctx); err != nil {
		return err
	}
	if err := s.client.RecordLogin(); err != nil {
		fmt.Fprintf(s.app.Err, "Aviso: %v\n", err)
	}
	return nil
}

func (s *portalSession) Close(ctx context.Context) error { return s.app.disconnect(ctx, s.client) }

func (a *App) openProxyLog(path string) (io.Writer, func(), error) {
	if path == "" || path == "-" {
		return a.Out, func() {}, nil
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("abrir el archivo de logs: %w", err)
	}
	return file, func() { file.Close() }, nil
}

func splitList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	list := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			list = append(list, trimmed)
		}
	}
	return list
}
