package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// importMarkerKey es una marca explícita y no "la tabla sigue vacía": si no,
// borrar la última cuenta haría que el siguiente comando reimportara todas.
const importMarkerKey = "nautapy-imported"

type ImportReport struct {
	Users       int
	Connections int
	Skipped     int
	// Pending: NautaGo ya tenía datos y no se importó nada. No se deja marca,
	// para reintentar cuando el almacén esté vacío.
	Pending bool
}

func (r ImportReport) Any() bool { return r.Users > 0 || r.Connections > 0 }

// ImportLegacy corre en cada arranque; la marca en el almacén garantiza que cada
// base se importe una sola vez. Las bases de NautaPy se abren en solo lectura
// para que NautaPy siga funcionando con sus datos.
func (s *Store) ImportLegacy(legacyDir string) (ImportReport, error) {
	var report ImportReport
	if legacyDir == "" {
		return report, nil
	}
	if _, err := os.Stat(legacyDir); err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, fmt.Errorf("inspect legacy directory: %w", err)
	}

	users, err := s.importUsers(filepath.Join(legacyDir, "users.db"))
	report.Users, report.Pending = users.Users, users.Pending
	if err != nil {
		return report, err
	}

	connections, err := s.importConnections(filepath.Join(legacyDir, "connections.db"))
	report.Connections, report.Skipped = connections.Connections, connections.Skipped
	report.Pending = report.Pending || connections.Pending
	if err != nil {
		return report, err
	}
	return report, nil
}

func (s *Store) importUsers(path string) (ImportReport, error) {
	var report ImportReport
	// La marca se mira antes de abrir la base de NautaPy para que, ya importado,
	// cada arranque cueste una sola consulta.
	tx, err := s.users.Begin()
	if err != nil {
		return report, fmt.Errorf("import users: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	marked, err := importMarked(tx)
	if err != nil {
		return report, err
	}
	if marked {
		return report, nil
	}
	if !emptyTable(tx, "users") {
		// Sin marcar: marcar aquí descartaría para siempre las cuentas de NautaPy
		// solo por el orden de instalación.
		report.Pending = true
		return report, nil
	}

	legacy, err := openLegacyDB(path, legacyUserSchema)
	if err != nil || legacy == nil {
		// Sin marca, para recoger una base de NautaPy que aparezca más tarde.
		return report, err
	}
	defer legacy.Close()

	records, legacyDefault, err := readLegacyUsers(legacy)
	if err != nil {
		return report, err
	}
	if len(records) == 0 {
		return report, nil
	}

	secrets, err := s.sealer()
	if err != nil {
		return report, err
	}
	for _, record := range records {
		password, err := secrets.open(record.password)
		if err != nil {
			return report, fmt.Errorf("import user %q: %w", record.name, err)
		}
		sealed, err := secrets.seal(password)
		if err != nil {
			return report, err
		}
		if _, err := tx.Exec("INSERT INTO users VALUES (?, ?)", record.name, sealed); err != nil {
			return report, fmt.Errorf("import user %q: %w", record.name, err)
		}
	}
	// NautaPy guardaba el predeterminado tal cual se escribió y lo resolvía por
	// prefijo. Se resuelve con la misma consulta que NautaGo para que el
	// resultado no pueda diferir.
	var def string
	if legacyDefault != "" {
		if def, err = resolveUserIn(tx, legacyDefault); err != nil {
			return report, fmt.Errorf("import default user: %w", err)
		}
	}
	if def != "" {
		if _, err := tx.Exec("INSERT INTO default_user VALUES (?)", def); err != nil {
			return report, fmt.Errorf("import default user: %w", err)
		}
	}
	if err := recordImport(tx); err != nil {
		return report, err
	}
	report.Users = len(records)
	return report, nil
}

type legacyUser struct {
	name     string
	password string
}

func readLegacyUsers(legacy *sql.DB) ([]legacyUser, string, error) {
	rows, err := legacy.Query("SELECT user, password FROM users ORDER BY rowid")
	if err != nil {
		return nil, "", fmt.Errorf("read legacy users: %w", err)
	}
	defer rows.Close()

	var records []legacyUser
	for rows.Next() {
		var user string
		var stored any
		if err := rows.Scan(&user, &stored); err != nil {
			return nil, "", fmt.Errorf("read legacy users: %w", err)
		}
		records = append(records, legacyUser{name: user, password: legacyPassword(stored)})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("read legacy users: %w", err)
	}

	// La tabla puede no existir, y eso equivale a "ninguno". Cualquier otro fallo
	// se devuelve para no dar por buena una importación sin predeterminado.
	var def sql.NullString
	switch err := legacy.QueryRow("SELECT user FROM default_user LIMIT 1").Scan(&def); {
	case err == nil, errors.Is(err, sql.ErrNoRows), isMissingTable(err):
	default:
		return nil, "", fmt.Errorf("read legacy default user: %w", err)
	}
	return records, def.String, nil
}

func (s *Store) importConnections(path string) (ImportReport, error) {
	var report ImportReport
	tx, err := s.connections.Begin()
	if err != nil {
		return report, fmt.Errorf("import connections: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	marked, err := importMarked(tx)
	if err != nil {
		return report, err
	}
	if marked {
		return report, nil
	}
	if !emptyTable(tx, "connections") {
		report.Pending = true
		return report, nil
	}

	legacy, err := openLegacyDB(path, legacyConnectionSchema)
	if err != nil || legacy == nil {
		return report, err
	}
	defer legacy.Close()

	// CAST porque el driver reescribe las columnas DATETIME como RFC 3339 en UTC,
	// y NautaPy guardó hora local.
	rows, err := legacy.Query(`
		SELECT user,
		       CAST(fecha_inicio_sesion AS TEXT),
		       CAST(fecha_cierre_sesion AS TEXT)
		FROM connections ORDER BY fecha_inicio_sesion`)
	if err != nil {
		return report, fmt.Errorf("read legacy connections: %w", err)
	}
	defer rows.Close()

	type record struct {
		user  string
		start string
		end   any
	}
	var records []record
	for rows.Next() {
		var user, start string
		var end sql.NullString
		if err := rows.Scan(&user, &start, &end); err != nil {
			return report, fmt.Errorf("read legacy connections: %w", err)
		}
		// Una fecha inventada es peor que una fila perdida, y una conexión sin
		// cierre válido quedaría abierta para siempre. La pérdida se cuenta.
		startTime, err := parseTime(start)
		if err != nil {
			report.Skipped++
			continue
		}
		var endValue any
		if end.Valid {
			endTime, err := parseTime(end.String)
			if err != nil {
				report.Skipped++
				continue
			}
			endValue = formatTime(endTime)
		}
		records = append(records, record{user: user, start: formatTime(startTime), end: endValue})
	}
	if err := rows.Err(); err != nil {
		return report, fmt.Errorf("read legacy connections: %w", err)
	}
	if len(records) == 0 {
		return report, nil
	}

	for _, record := range records {
		if _, err := tx.Exec(
			"INSERT INTO connections (user, fecha_inicio_sesion, fecha_cierre_sesion) VALUES (?, ?, ?)",
			record.user, record.start, record.end,
		); err != nil {
			return report, fmt.Errorf("import connection for %q: %w", record.user, err)
		}
	}
	if err := recordImport(tx); err != nil {
		return report, err
	}
	report.Connections = len(records)
	return report, nil
}

func importMarked(tx *sql.Tx) (bool, error) {
	var value string
	err := tx.QueryRow("SELECT value FROM meta WHERE key = ?", importMarkerKey).Scan(&value)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("read import marker: %w", err)
	}
}

// La marca va en la misma transacción que las filas para que nunca discrepen.
func recordImport(tx *sql.Tx) error {
	if _, err := tx.Exec("INSERT OR REPLACE INTO meta (key, value) VALUES (?, ?)", importMarkerKey, "1"); err != nil {
		return fmt.Errorf("record import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record import: %w", err)
	}
	return nil
}

func emptyTable(tx *sql.Tx, table string) bool {
	var count int
	if err := tx.QueryRow("SELECT count(1) FROM " + table).Scan(&count); err != nil {
		return false
	}
	return count == 0
}

// openLegacyDB devuelve nil, sin error, si la base no existe o no es de NautaPy.
// El esquema hace de sonda: CREATE TABLE IF NOT EXISTS no hace nada si la tabla
// existe, y en modo solo lectura falla si no existe.
func openLegacyDB(path string, schema []string) (*sql.DB, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect legacy database: %w", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open legacy database: %w", err)
	}
	db.SetMaxOpenConns(1)

	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, nil
		}
	}
	return db, nil
}

func legacyPassword(stored any) string {
	switch value := stored.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}

// El driver no expone un código de error para "no such table".
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}
