package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func writeLegacyDatabases(t *testing.T, dir string, start, end time.Time) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}

	users, err := sql.Open("sqlite", filepath.Join(dir, "users.db"))
	if err != nil {
		t.Fatalf("open legacy users: %v", err)
	}
	defer users.Close()
	mustExec(t, users, "CREATE TABLE users (user TEXT, password TEXT)")
	mustExec(t, users, "CREATE TABLE default_user (user TEXT)")
	mustExec(t, users, "INSERT INTO users VALUES (?, ?)", "plinkr@nauta.com.cu", []byte("OF(pTWiT@^I59alF#"))
	mustExec(t, users, "INSERT INTO default_user VALUES (?)", "plinkr@nauta.com.cu")
	users.Close()

	connections, err := sql.Open("sqlite", filepath.Join(dir, "connections.db"))
	if err != nil {
		t.Fatalf("open legacy connections: %v", err)
	}
	defer connections.Close()
	mustExec(t, connections, `CREATE TABLE connections (
		user TEXT, fecha_inicio_sesion DATETIME, fecha_cierre_sesion DATETIME)`)
	mustExec(t, connections, "INSERT INTO connections VALUES (?, ?, ?)",
		"plinkr@nauta.com.cu",
		start.UTC().Format(time.RFC3339Nano),
		end.UTC().Format(time.RFC3339Nano),
	)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func TestImportLegacyBringsUsersAndHistory(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "nautapy")
	start := time.Date(2026, time.March, 4, 10, 0, 0, 0, time.UTC)
	writeLegacyDatabases(t, legacy, start, start.Add(25*time.Minute))

	store, err := OpenAt(filepath.Join(root, "nautago"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	report, err := store.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if !report.Any() {
		t.Fatal("ImportLegacy reported nothing to import")
	}
	if report.Users != 1 || report.Connections != 1 || report.Skipped != 0 {
		t.Errorf("report = %+v, want 1 user, 1 connection, 0 skipped", report)
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "plinkr@nauta.com.cu" {
		t.Fatalf("ListUsers = %v, want [plinkr@nauta.com.cu]", users)
	}

	def, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if def != "plinkr@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want plinkr@nauta.com.cu", def)
	}

	// Venía en base85: debe leerse y quedar cifrada.
	name, password, err := store.FindCredentials("plinkr", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if name != "plinkr@nauta.com.cu" || password != "K@tre03081971" {
		t.Errorf("FindCredentials = %q/%q, want plinkr@nauta.com.cu/K@tre03081971", name, password)
	}

	var stored string
	if err := store.users.QueryRow("SELECT password FROM users").Scan(&stored); err != nil {
		t.Fatalf("read stored password: %v", err)
	}
	if stored == "K@tre03081971" {
		t.Error("the imported password was stored in clear text")
	}

	// Las marcas RFC 3339 deben normalizarse para que los listados las lean.
	all, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Connections returned %d rows, want 1", len(all))
	}
	if got := all[0].Start.UTC().Format(time.RFC3339); got != start.Format(time.RFC3339) {
		t.Errorf("start = %s, want %s", got, start)
	}
	if got := all[0].End.UTC().Format(time.RFC3339); got != end(start).Format(time.RFC3339) {
		t.Errorf("end = %s, want %s", got, end(start))
	}
}

func end(start time.Time) time.Time { return start.Add(25 * time.Minute) }

func TestImportLegacyRunsOnlyOnce(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "nautapy")
	writeLegacyDatabases(t, legacy, time.Now(), time.Now().Add(time.Hour))

	store, err := OpenAt(filepath.Join(root, "nautago"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	if _, err := store.ImportLegacy(legacy); err != nil {
		t.Fatalf("first ImportLegacy: %v", err)
	}
	report, err := store.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("second ImportLegacy: %v", err)
	}
	if report.Any() {
		t.Error("a second import claimed to have imported something")
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("ListUsers = %v, want exactly one user", users)
	}
	connections, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 1 {
		t.Errorf("Connections returned %d rows, want 1", len(connections))
	}
}

func TestImportLegacyKeepsNautaPyUsersPendingWhenTheStoreIsNotEmpty(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "nautapy")
	writeLegacyDatabases(t, legacy, time.Now(), time.Now().Add(time.Hour))

	dir := filepath.Join(root, "nautago")
	store, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if err := store.AddUser("nautago@nauta.com.cu", "pass"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	store.Close()

	store, err = OpenAt(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	report, err := store.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if !report.Pending {
		t.Error("the report did not say the NautaPy users were left behind")
	}
	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 1 || users[0] != "nautago@nauta.com.cu" {
		t.Errorf("ListUsers = %v, want only the NautaGo user", users)
	}

	// Sin marca, un almacén que se vacíe tiene que recogerlas después.
	store.Close()
	if err := os.Remove(filepath.Join(dir, "users.db")); err != nil {
		t.Fatalf("remove users database: %v", err)
	}
	empty, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer empty.Close()

	imported, err := empty.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("ImportLegacy on an empty store: %v", err)
	}
	if imported.Users == 0 {
		t.Error("the NautaPy users were never picked up once the store was empty")
	}
}

func TestImportLegacyWithoutLegacyDirectory(t *testing.T) {
	store, err := OpenAt(filepath.Join(t.TempDir(), "nautago"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	report, err := store.ImportLegacy(filepath.Join(t.TempDir(), "nowhere"))
	if err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if report.Any() {
		t.Error("importing from a missing directory reported an import")
	}
}

func TestImportLegacyKeepsRemovedUsersGone(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "nautapy")
	start := time.Date(2026, time.March, 4, 10, 0, 0, 0, time.UTC)
	writeLegacyDatabases(t, legacy, start, start.Add(25*time.Minute))

	store, err := OpenAt(filepath.Join(root, "nautago"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	if _, err := store.ImportLegacy(legacy); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if err := store.RemoveUser("plinkr@nauta.com.cu"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	report, err := store.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("second ImportLegacy: %v", err)
	}
	if report.Any() {
		t.Error("the removed user was imported again")
	}
	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("ListUsers = %v, want none", users)
	}
}

func TestImportLegacySkipsUnreadableTimestamps(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "nautapy")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db, err := sql.Open("sqlite", filepath.Join(legacy, "connections.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustExec(t, db, `CREATE TABLE connections (
		user TEXT, fecha_inicio_sesion DATETIME, fecha_cierre_sesion DATETIME)`)
	// Un par legible, un inicio ilegible y un final ilegible.
	mustExec(t, db, `INSERT INTO connections VALUES
		('good', '2026-03-04 10:00:00.000000', '2026-03-04 10:25:00.000000'),
		('bad start', 'not a date', '2026-03-04 10:25:00.000000'),
		('bad end', '2026-03-04 11:00:00.000000', 'not a date')`)
	db.Close()

	store, err := OpenAt(filepath.Join(root, "nautago"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	report, err := store.ImportLegacy(legacy)
	if err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if report.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", report.Skipped)
	}
	if report.Connections != 1 {
		t.Errorf("Connections = %d, want 1", report.Connections)
	}

	connections, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 1 || connections[0].User != "good" {
		t.Errorf("Connections = %+v, want only the readable row", connections)
	}
}

// Las bases reales de NautaPy traen texto plano en hora local o RFC 3339 en UTC;
// ambas deben acabar en hora local.
func TestImportLegacyNormalisesLegacyTimestamps(t *testing.T) {
	local := time.Date(2024, 9, 22, 6, 13, 10, 883754000, time.Local)
	rows := []struct {
		name   string
		stored string
		want   time.Time
	}{
		{
			name:   "local wall clock",
			stored: local.Format(timestampLayout),
			want:   local,
		},
		{
			name:   "rfc 3339 utc",
			stored: local.UTC().Format(time.RFC3339Nano),
			want:   local,
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			root := t.TempDir()
			legacy := filepath.Join(root, "nautapy")
			if err := os.MkdirAll(legacy, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			db, err := sql.Open("sqlite", filepath.Join(legacy, "connections.db"))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			mustExec(t, db, `CREATE TABLE connections (
				user TEXT, fecha_inicio_sesion DATETIME, fecha_cierre_sesion DATETIME)`)
			// Un literal evita que el driver reescriba el valor al entrar.
			mustExec(t, db, fmt.Sprintf(
				"INSERT INTO connections VALUES ('someone', '%s', '%s')", row.stored, row.stored))
			db.Close()

			store, err := OpenAt(filepath.Join(root, "nautago"))
			if err != nil {
				t.Fatalf("OpenAt: %v", err)
			}
			defer store.Close()

			if _, err := store.ImportLegacy(legacy); err != nil {
				t.Fatalf("ImportLegacy: %v", err)
			}
			connections, err := store.Connections()
			if err != nil {
				t.Fatalf("Connections: %v", err)
			}
			if len(connections) != 1 {
				t.Fatalf("Connections returned %d rows, want 1", len(connections))
			}
			if !connections[0].Start.Equal(row.want) {
				t.Errorf("start = %s, want %s", connections[0].Start, row.want)
			}
			if connections[0].End == nil || !connections[0].End.Equal(row.want) {
				t.Errorf("end = %v, want %s", connections[0].End, row.want)
			}
		})
	}
}

// NautaPy resolvía el predeterminado con LIKE: por prefijo y sin distinguir
// mayúsculas. Un `set-default Pepe` no puede perderse al importar.
func TestImportLegacyResolvesTheStoredDefault(t *testing.T) {
	cases := map[string]string{
		"prefix":      "pepe",
		"ignore case": "Pepe",
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			legacy := filepath.Join(root, "nautapy")
			if err := os.MkdirAll(legacy, 0o755); err != nil {
				t.Fatalf("mkdir legacy: %v", err)
			}
			users, err := sql.Open("sqlite", filepath.Join(legacy, "users.db"))
			if err != nil {
				t.Fatalf("open legacy users: %v", err)
			}
			mustExec(t, users, "CREATE TABLE users (user TEXT, password TEXT)")
			mustExec(t, users, "CREATE TABLE default_user (user TEXT)")
			mustExec(t, users, "INSERT INTO users VALUES (?, ?)", "plinkr@nauta.com.cu", []byte("OF(pTWiT@^I59alF#"))
			mustExec(t, users, "INSERT INTO users VALUES (?, ?)", "pepe@nauta.com.cu", []byte("OF(pTWiT@^I59alF#"))
			mustExec(t, users, "INSERT INTO default_user VALUES (?)", stored)
			users.Close()

			store, err := OpenAt(filepath.Join(root, "nautago"))
			if err != nil {
				t.Fatalf("OpenAt: %v", err)
			}
			defer store.Close()
			if _, err := store.ImportLegacy(legacy); err != nil {
				t.Fatalf("ImportLegacy: %v", err)
			}

			def, err := store.DefaultUser()
			if err != nil {
				t.Fatalf("DefaultUser: %v", err)
			}
			if def != "pepe@nauta.com.cu" {
				t.Errorf("DefaultUser = %q, want pepe@nauta.com.cu", def)
			}
		})
	}
}
