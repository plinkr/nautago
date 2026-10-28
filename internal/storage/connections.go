package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Connection es un par de entrada y salida. End es nil mientras la sesión sigue
// abierta.
type Connection struct {
	User  string
	Start time.Time
	End   *time.Time
}

func (s *Store) SaveLogin(user string, at time.Time) error {
	_, err := s.connections.Exec(
		"INSERT INTO connections (user, fecha_inicio_sesion) VALUES (?, ?)",
		user, formatTime(at),
	)
	if err != nil {
		return fmt.Errorf("record login: %w", err)
	}
	return nil
}

// Sin conexión abierta que cerrar no se escribe nada: el historial nunca debe
// bloquear una desconexión.
func (s *Store) SaveLogout(user string, at time.Time) error {
	_, err := s.connections.Exec(`
		UPDATE connections
		SET fecha_cierre_sesion = ?
		WHERE user = ? AND fecha_cierre_sesion IS NULL
		AND fecha_inicio_sesion = (
			SELECT MAX(fecha_inicio_sesion) FROM connections
			WHERE user = ? AND fecha_cierre_sesion IS NULL
		)`, formatTime(at), user, user)
	if err != nil {
		return fmt.Errorf("record logout: %w", err)
	}
	return nil
}

func (s *Store) Connections() ([]Connection, error) {
	return s.connectionsBetween("", "")
}

func (s *Store) ConnectionsOfMonth(at time.Time) ([]Connection, error) {
	start, end := monthBounds(at)
	return s.connectionsBetween(formatTime(start), formatTime(end))
}

func (s *Store) ConnectionsOfPreviousMonth(at time.Time) ([]Connection, error) {
	start, _ := monthBounds(at)
	prevStart, prevEnd := start.AddDate(0, -1, 0), start
	return s.connectionsBetween(formatTime(prevStart), formatTime(prevEnd))
}

// Rango [from, until). Un límite vacío no acota ese lado.
func (s *Store) connectionsBetween(from, until string) ([]Connection, error) {
	query := `SELECT user, fecha_inicio_sesion, fecha_cierre_sesion FROM connections`
	var where []string
	var args []any
	if from != "" {
		where = append(where, "fecha_inicio_sesion >= ?")
		args = append(args, from)
	}
	if until != "" {
		where = append(where, "fecha_inicio_sesion < ?")
		args = append(args, until)
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY fecha_inicio_sesion"

	rows, err := s.connections.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list connections: %w", err)
	}
	defer rows.Close()

	var connections []Connection
	for rows.Next() {
		var user, start string
		var end sql.NullString
		if err := rows.Scan(&user, &start, &end); err != nil {
			return nil, fmt.Errorf("list connections: %w", err)
		}

		startTime, err := parseTime(start)
		if err != nil {
			return nil, fmt.Errorf("list connections: unreadable start time %q: %w", start, err)
		}
		conn := Connection{User: user, Start: startTime}

		if end.Valid {
			endTime, err := parseTime(end.String)
			if err != nil {
				return nil, fmt.Errorf("list connections: unreadable end time %q: %w", end.String, err)
			}
			conn.End = &endTime
		}
		connections = append(connections, conn)
	}
	return connections, rows.Err()
}

func monthBounds(at time.Time) (start, end time.Time) {
	loc := at.Location()
	start = time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 1, 0)
}
