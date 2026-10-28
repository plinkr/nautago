package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrNoUser = errors.New("no user stored")

// DefaultUser cae al primer usuario guardado, como NautaPy. El predeterminado se
// resuelve por prefijo porque NautaPy guardaba lo que se escribiera en
// `set-default`, y debe existir como cuenta: si no, cada comando pediría la
// contraseña de un usuario borrado.
func (s *Store) DefaultUser() (string, error) {
	var stored string
	err := s.users.QueryRow("SELECT user FROM default_user LIMIT 1").Scan(&stored)
	switch {
	case err == nil:
		name, err := s.resolveUser(stored)
		if err != nil {
			return "", err
		}
		if name != "" {
			return name, nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf("read default user: %w", err)
	}

	var user string
	err = s.users.QueryRow("SELECT user FROM users ORDER BY rowid LIMIT 1").Scan(&user)
	switch {
	case err == nil:
		return user, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrNoUser
	default:
		return "", fmt.Errorf("read users: %w", err)
	}
}

// FindCredentials compara por prefijo para que `nauta up plinkr` encuentre
// `plinkr@nauta.com.cu`, como en NautaPy. Los comodines de LIKE se escapan y
// gana la cuenta más antigua, para que el resultado sea determinista.
func (s *Store) FindCredentials(user, password string) (string, string, error) {
	var name, stored string
	query := `SELECT user, password FROM users WHERE user LIKE ? ESCAPE '\' ORDER BY rowid LIMIT 1`
	err := s.users.QueryRow(query, likePrefix(user)).Scan(&name, &stored)
	switch {
	case err == nil:
	case errors.Is(err, sql.ErrNoRows):
		return user, password, nil
	default:
		return "", "", fmt.Errorf("read user: %w", err)
	}

	secrets, err := s.sealer()
	if err != nil {
		return "", "", err
	}
	plain, err := secrets.open(stored)
	if err != nil {
		return "", "", err
	}
	return name, plain, nil
}

func (s *Store) resolveUser(user string) (string, error) {
	return resolveUserIn(s.users, user)
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func resolveUserIn(db rowQuerier, user string) (string, error) {
	var name string
	err := db.QueryRow(`SELECT user FROM users WHERE user LIKE ? ESCAPE '\' ORDER BY rowid LIMIT 1`,
		likePrefix(user)).Scan(&name)
	switch {
	case err == nil:
		return name, nil
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	default:
		return "", fmt.Errorf("read user: %w", err)
	}
}

func likePrefix(user string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(user)
	return escaped + "%"
}

// AddUser permite nombres repetidos porque NautaPy los permitía.
func (s *Store) AddUser(user, password string) error {
	secrets, err := s.sealer()
	if err != nil {
		return err
	}
	sealed, err := secrets.seal(password)
	if err != nil {
		return err
	}
	if _, err := s.users.Exec("INSERT INTO users VALUES (?, ?)", user, sealed); err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// SetDefaultUser guarda el nombre completo resuelto, para que no cambie de cuenta
// si luego se agrega otra con el mismo prefijo y para que RemoveUser lo
// encuentre. Un nombre sin cuenta se guarda tal cual, como en NautaPy.
func (s *Store) SetDefaultUser(user string) error {
	resolved, err := s.resolveUser(user)
	if err != nil {
		return err
	}
	if resolved != "" {
		user = resolved
	}

	var count int
	if err := s.users.QueryRow("SELECT count(user) FROM default_user").Scan(&count); err != nil {
		return fmt.Errorf("read default user: %w", err)
	}

	if count > 0 {
		_, err = s.users.Exec("UPDATE default_user SET user=?", user)
	} else {
		_, err = s.users.Exec("INSERT INTO default_user VALUES (?)", user)
	}
	if err != nil {
		return fmt.Errorf("store default user: %w", err)
	}
	return nil
}

// SetPassword sobre un usuario inexistente no es un error, como en NautaPy. Es
// también la recuperación tras perder la clave, pero solo crea una nueva si hay
// una cuenta que actualizar: un nombre mal escrito haría desaparecer el aviso
// de clave perdida sin haber recuperado nada.
func (s *Store) SetPassword(user, password string) error {
	secrets, err := s.sealer()
	if errors.Is(err, errKeyLost) {
		var exists int
		switch err := s.users.QueryRow("SELECT 1 FROM users WHERE user=? LIMIT 1", user).Scan(&exists); {
		case errors.Is(err, sql.ErrNoRows):
			return nil
		case err != nil:
			return fmt.Errorf("read user: %w", err)
		}
		secrets, err = s.resetSealer()
	}
	if err != nil {
		return err
	}
	sealed, err := secrets.seal(password)
	if err != nil {
		return err
	}
	if _, err := s.users.Exec("UPDATE users SET password=? WHERE user=?", sealed, user); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// El predeterminado se borra en la misma transacción para que nunca nombre una
// cuenta que ya no existe.
func (s *Store) RemoveUser(user string) error {
	tx, err := s.users.Begin()
	if err != nil {
		return fmt.Errorf("remove user: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec("DELETE FROM users WHERE user=?", user); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if _, err := tx.Exec("DELETE FROM default_user WHERE user=?", user); err != nil {
		return fmt.Errorf("clear default user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remove user: %w", err)
	}
	return nil
}

func (s *Store) ListUsers() ([]string, error) {
	rows, err := s.users.Query("SELECT user FROM users")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []string
	for rows.Next() {
		var user string
		if err := rows.Scan(&user); err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}
