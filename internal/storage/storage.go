// Package storage guarda usuarios, credenciales e historial de conexiones. El SQL
// no sale de este paquete.
package storage

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	_ "modernc.org/sqlite" // Go puro: el binario no necesita cgo.
)

// timestampLayout es el formato de NautaPy, para que las bases sean
// intercambiables, y ordena lexicográficamente igual que cronológicamente.
const timestampLayout = "2006-01-02 15:04:05.000000"

const fileMode = 0o600

const dirMode = 0o700

const busyTimeout = 5 * time.Second

type Store struct {
	dir string

	users       *sql.DB
	connections *sql.DB

	keyPath string
	secrets *keyring
	keyErr  error
}

func Dir() string {
	if dir := os.Getenv("NAUTAGO_HOME"); dir != "" {
		return dir
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "nautago")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(home, "AppData", "Roaming", "nautago")
	}
	return filepath.Join(home, ".local", "share", "nautago")
}

func Open() (*Store, error) {
	return OpenAt(Dir())
}

func OpenAt(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("no data directory available: set NAUTAGO_HOME or HOME")
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	s := &Store{dir: dir}

	// La base de usuarios va primero porque decide si se puede crear una clave
	// nueva: hacerlo sobre contraseñas ya cifradas las dejaría ilegibles.
	var err error
	if s.users, err = openDB(filepath.Join(dir, "users.db"), userSchema); err != nil {
		return nil, fmt.Errorf("open users database: %w", err)
	}

	// Un problema con la clave no impide abrir el almacén; lo devuelve cada
	// operación que la necesita. Si no, bloquearía `nauta down` con una sesión
	// facturando y el `users set-password` que el propio error recomienda.
	s.keyPath = filepath.Join(dir, "credentials.key")
	if s.keyErr = ensureKeyRecoverable(s.keyPath, s.users); s.keyErr == nil {
		s.secrets, s.keyErr = openKeyring(s.keyPath)
	}

	if s.connections, err = openDB(filepath.Join(dir, "connections.db"), connectionSchema); err != nil {
		s.users.Close()
		return nil, fmt.Errorf("open connections database: %w", err)
	}

	return s, nil
}

func (s *Store) Dir() string { return s.dir }

func (s *Store) Close() error {
	var firstErr error
	for _, db := range []*sql.DB{s.users, s.connections} {
		if db == nil {
			continue
		}
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var userSchema = []string{
	`CREATE TABLE IF NOT EXISTS users (user TEXT, password TEXT)`,
	`CREATE TABLE IF NOT EXISTS default_user (user TEXT)`,
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
}

// Las fechas son TEXT a propósito: con DATETIME el driver reescribe los valores,
// pierde la hora local y rompe los filtros de mes. NautaPy declaraba DATETIME
// pero guardaba texto, así que sus datos siguen siendo legibles.
var connectionSchema = []string{
	`CREATE TABLE IF NOT EXISTS connections (
		user TEXT,
		fecha_inicio_sesion TEXT,
		fecha_cierre_sesion TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS connections_start ON connections (fecha_inicio_sesion)`,
	`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT)`,
}

// Deben coincidir exactamente con las tablas de NautaPy: solo sirven para
// reconocer sus bases.
var legacyUserSchema = []string{
	`CREATE TABLE IF NOT EXISTS users (user TEXT, password TEXT)`,
	`CREATE TABLE IF NOT EXISTS default_user (user TEXT)`,
}

var legacyConnectionSchema = []string{
	`CREATE TABLE IF NOT EXISTS connections (
		user TEXT,
		fecha_inicio_sesion TEXT,
		fecha_cierre_sesion TEXT
	)`,
}

func openDB(path string, schema []string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	// Un solo escritor por proceso; una conexión evita contención interna.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err := os.Chmod(path, fileMode); err != nil && !os.IsNotExist(err) {
		db.Close()
		return nil, err
	}
	return db, nil
}

// El busy timeout va en el DSN y no en un PRAGMA porque se aplica a cada
// conexión que abra el pool. Hace falta porque `nauta down` desde otra terminal
// escribe las mismas bases que el `nauta up` en curso.
func dsn(path string) string {
	query := url.Values{"_busy_timeout": {strconv.FormatInt(int64(busyTimeout/time.Millisecond), 10)}}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}

func formatTime(t time.Time) string { return t.Format(timestampLayout) }

// Se aceptan otros formatos porque una base editada a mano o por otras
// herramientas puede traerlos; todo se expresa en hora local.
func parseTime(value string) (time.Time, error) {
	for _, layout := range timestampLayouts {
		if t, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return t.In(time.Local), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", value)
}

var timestampLayouts = []string{
	timestampLayout,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02",
}
