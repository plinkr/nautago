package cli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

func (a *App) runConnected(ctx context.Context, argv []string) error {
	user, password, command, err := splitRunConnected(argv)
	if err != nil {
		return err
	}
	if len(command) == 0 {
		return errors.New("nauta run-connected requiere un comando")
	}

	name, secret, err := a.credentials(user, password)
	if err != nil {
		return err
	}

	client := a.newClient(name, secret)
	if err := a.guardTunnelsBeforeLogin(ctx, client); err != nil {
		return err
	}
	if err := client.Login(ctx); err != nil {
		return err
	}

	runErr := a.execute(ctx, command)

	// Se cierra aunque el comando falle, y sin heredar la cancelación de la
	// interrupción: si no, la sesión quedaría abierta.
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	logoutErr := a.disconnect(shutdown, client)
	cancel()

	if runErr != nil {
		return runErr
	}
	return logoutErr
}

// Desde el primer argumento que no es una opción, todo pertenece al programa a
// ejecutar, opciones incluidas.
func splitRunConnected(argv []string) (user, password string, command []string, err error) {
	rest := argv
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		name := strings.TrimLeft(rest[0], "-")
		if index := strings.IndexByte(name, '='); index >= 0 {
			value := name[index+1:]
			name = name[:index]
			rest = rest[1:]
			if err := assignRunConnectedOption(name, value, &user, &password); err != nil {
				return "", "", nil, err
			}
			continue
		}
		if name != "u" && name != "user" && name != "p" && name != "password" {
			break
		}
		if len(rest) < 2 {
			return "", "", nil, fmt.Errorf("nauta run-connected: falta el valor de %s", rest[0])
		}
		value := rest[1]
		rest = rest[2:]
		if err := assignRunConnectedOption(name, value, &user, &password); err != nil {
			return "", "", nil, err
		}
	}
	return user, password, rest, nil
}

func assignRunConnectedOption(name, value string, user, password *string) error {
	switch name {
	case "u", "user":
		*user = value
	case "p", "password":
		*password = value
	default:
		return fmt.Errorf("nauta run-connected: opción desconocida %q", name)
	}
	return nil
}

// Sin shell, para que los argumentos signifiquen exactamente lo que se escribió.
func (a *App) execute(ctx context.Context, command []string) error {
	program := exec.CommandContext(ctx, command[0], command[1:]...)
	program.Stdin = a.In
	program.Stdout = a.Out
	program.Stderr = a.Err

	err := program.Run()
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &commandFailure{code: exitErr.ExitCode()}
	}
	// 127 es la convención de la shell para "comando no encontrado".
	return &commandFailure{code: 127, err: err}
}

// Si el comando llegó a ejecutarse, su salida ya explicó el fallo y Run solo
// propaga el código. Si no se pudo iniciar, err lleva la causa y Run la muestra.
type commandFailure struct {
	code int
	err  error
}

func (e *commandFailure) Error() string {
	if e.err != nil {
		return fmt.Sprintf("nauta run-connected: %v", e.err)
	}
	return fmt.Sprintf("nauta run-connected: el comando terminó con estado %d", e.code)
}
