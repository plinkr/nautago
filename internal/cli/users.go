package cli

import (
	"errors"
	"fmt"

	"golang.org/x/term"
)

func (a *App) users(argv []string) error {
	if len(argv) == 0 {
		a.printUsersUsage()
		return errUsageRequested
	}

	command, rest := splitCommand(argv)
	switch command {
	case "add":
		return a.addUser(rest)
	case "set-default":
		return a.setDefaultUser(rest)
	case "set-password":
		return a.setPassword(rest)
	case "remove":
		return a.removeUser(rest)
	case "list":
		return a.listUsers(rest)
	case "-h", "--help":
		a.printUsersUsage()
		return errUsageRequested
	default:
		return fmt.Errorf("subcomando desconocido: users %s", command)
	}
}

func (a *App) addUser(argv []string) error {
	cmd := a.newCommand("users add", "nauta users add [-h] usuario [contraseña]",
		"Guarda un usuario Nauta.")
	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("nauta users add requiere un usuario")
	}
	if err := atMost(args, 2, "nauta users add"); err != nil {
		return err
	}
	user := args[0]

	password, err := a.passwordFor(user, argumentAt(args, 1))
	if err != nil {
		return err
	}
	if err := a.Store.AddUser(user, password); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Usuario guardado: %s\n", user)
	return nil
}

func (a *App) setDefaultUser(argv []string) error {
	cmd := a.newCommand("users set-default", "nauta users set-default [-h] usuario",
		"Marca un usuario Nauta como predeterminado.")
	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("nauta users set-default requiere exactamente un usuario")
	}
	user := args[0]

	if err := a.Store.SetDefaultUser(user); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Usuario predeterminado: %s\n", user)
	return nil
}

func (a *App) setPassword(argv []string) error {
	cmd := a.newCommand("users set-password", "nauta users set-password [-h] usuario [contraseña]",
		"Cambia la contraseña de un usuario Nauta.")
	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if len(args) < 1 {
		return errors.New("nauta users set-password requiere un usuario")
	}
	if err := atMost(args, 2, "nauta users set-password"); err != nil {
		return err
	}
	user := args[0]

	password, err := a.passwordFor(user, argumentAt(args, 1))
	if err != nil {
		return err
	}
	if err := a.Store.SetPassword(user, password); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Contraseña actualizada: %s\n", user)
	return nil
}

func (a *App) removeUser(argv []string) error {
	cmd := a.newCommand("users remove", "nauta users remove [-h] usuario",
		"Elimina un usuario Nauta.")
	args, err := cmd.parse(a, argv)
	if err != nil {
		return err
	}
	if len(args) != 1 {
		return errors.New("nauta users remove requiere exactamente un usuario")
	}
	user := args[0]

	if err := a.Store.RemoveUser(user); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Usuario eliminado: %s\n", user)
	return nil
}

func (a *App) listUsers(argv []string) error {
	cmd := a.newCommand("users list", "nauta users list", "Lista los usuarios Nauta guardados.")
	if _, err := cmd.parse(a, argv); err != nil {
		return err
	}

	users, err := a.Store.ListUsers()
	if err != nil {
		return err
	}
	for _, user := range users {
		fmt.Fprintln(a.Out, user)
	}
	return nil
}

func (a *App) printUsersUsage() {
	a.printText(`uso: nauta users [-h] {add,set-default,set-password,remove,list} ...

Gestiona los usuarios Nauta guardados.

comandos:
  add           Guarda un usuario Nauta.
  set-default   Marca un usuario Nauta como predeterminado.
  set-password  Cambia la contraseña de un usuario Nauta.
  remove        Elimina un usuario Nauta.
  list          Lista los usuarios Nauta guardados.`)
}

func (a *App) passwordFor(user, given string) (string, error) {
	if given != "" {
		return given, nil
	}
	return a.promptPassword(fmt.Sprintf("Contraseña para %s: ", user))
}

// Sin terminal la línea se lee tal cual, que es lo que permite pasar la
// contraseña por una tubería.
func (a *App) promptPassword(prompt string) (string, error) {
	fmt.Fprint(a.Err, prompt)

	if file, ok := a.In.(interface{ Fd() uintptr }); ok && term.IsTerminal(int(file.Fd())) {
		secret, err := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(a.Err)
		if err != nil {
			return "", fmt.Errorf("leer la contraseña: %w", err)
		}
		return string(secret), nil
	}

	fmt.Fprintln(a.Err)
	secret, err := readLine(a.In)
	if err != nil {
		return "", fmt.Errorf("leer la contraseña: %w", err)
	}
	return secret, nil
}

func argumentAt(args []string, index int) string {
	if index < len(args) {
		return args[index]
	}
	return ""
}
