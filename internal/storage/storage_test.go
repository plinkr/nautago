package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenAt(t.TempDir())
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestDefaultUserFallsBackToTheFirstStored(t *testing.T) {
	store := newStore(t)

	if _, err := store.DefaultUser(); !errors.Is(err, ErrNoUser) {
		t.Fatalf("DefaultUser error = %v, want ErrNoUser", err)
	}

	if err := store.AddUser("first@nauta.com.cu", "pass"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	user, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "first@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the first stored user", user)
	}
}

func TestDefaultUserPrefersTheMarkedOne(t *testing.T) {
	store := newStore(t)
	for _, user := range []string{"a@nauta.com.cu", "b@nauta.com.cu"} {
		if err := store.AddUser(user, "pass"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}
	if err := store.SetDefaultUser("b@nauta.com.cu"); err != nil {
		t.Fatalf("SetDefaultUser: %v", err)
	}

	user, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "b@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the marked user", user)
	}

	if err := store.SetDefaultUser("a@nauta.com.cu"); err != nil {
		t.Fatalf("SetDefaultUser: %v", err)
	}
	user, err = store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "a@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the replaced choice", user)
	}
}

func TestFindCredentialsMatchesByPrefix(t *testing.T) {
	store := newStore(t)
	if err := store.AddUser("plinkr@nauta.com.cu", "s3cret"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	name, password, err := store.FindCredentials("plinkr", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if name != "plinkr@nauta.com.cu" {
		t.Errorf("name = %q, want the stored account", name)
	}
	if password != "s3cret" {
		t.Errorf("password = %q, want the stored password", password)
	}
}

func TestFindCredentialsFallsBackToTheGivenValues(t *testing.T) {
	store := newStore(t)

	name, password, err := store.FindCredentials("nuevo@nauta.com.cu", "typed")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if name != "nuevo@nauta.com.cu" || password != "typed" {
		t.Errorf("FindCredentials = %q/%q, want the given values", name, password)
	}
}

// Un '%' o '_' sin escapar haría que `nauta up` se autenticara, y facturara,
// como una cuenta que el usuario nunca nombró.
func TestFindCredentialsTreatsTheNameAsALiteralPrefix(t *testing.T) {
	store := newStore(t)
	for _, account := range []string{"plinkr@nauta.com.cu", "exposito@nauta.com.cu"} {
		if err := store.AddUser(account, "s3cret-"+account); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}

	for _, query := range []string{"%", "_", "plinkr_", "plink%", "nauta.com.cu"} {
		name, password, err := store.FindCredentials(query, "typed")
		if err != nil {
			t.Fatalf("FindCredentials(%q): %v", query, err)
		}
		if name != query {
			t.Errorf("FindCredentials(%q) = %q, want the name unchanged: a wildcard matched an account", query, name)
		}
		if password != "typed" {
			t.Errorf("FindCredentials(%q) returned the password of %q", query, name)
		}
	}
}

func TestFindCredentialsPicksTheOldestMatch(t *testing.T) {
	store := newStore(t)
	if err := store.AddUser("gemela@nauta.com.cu", "first"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := store.AddUser("gemela@nauta.com.cu", "second"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	for range 10 {
		name, password, err := store.FindCredentials("gemela", "")
		if err != nil {
			t.Fatalf("FindCredentials: %v", err)
		}
		if name != "gemela@nauta.com.cu" || password != "first" {
			t.Fatalf("FindCredentials = %q/%q, want the first stored account", name, password)
		}
	}
}

func TestSetPasswordReplacesTheStoredOne(t *testing.T) {
	store := newStore(t)
	if err := store.AddUser("plinkr@nauta.com.cu", "old"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := store.SetPassword("plinkr@nauta.com.cu", "new"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}

	_, password, err := store.FindCredentials("plinkr", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if password != "new" {
		t.Errorf("password = %q, want the new one", password)
	}
}

func TestSetPasswordOfAnUnknownUserIsNotAnError(t *testing.T) {
	store := newStore(t)
	if err := store.SetPassword("ghost@nauta.com.cu", "pass"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
}

func TestRemoveUser(t *testing.T) {
	store := newStore(t)
	if err := store.AddUser("plinkr@nauta.com.cu", "pass"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	if err := store.RemoveUser("plinkr@nauta.com.cu"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 0 {
		t.Errorf("ListUsers = %v, want none", users)
	}
}

func TestRemoveUserClearsTheDefaultItPointedAt(t *testing.T) {
	store := newStore(t)
	for _, user := range []string{"a@nauta.com.cu", "b@nauta.com.cu"} {
		if err := store.AddUser(user, "pass"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}
	if err := store.SetDefaultUser("a@nauta.com.cu"); err != nil {
		t.Fatalf("SetDefaultUser: %v", err)
	}
	if err := store.RemoveUser("a@nauta.com.cu"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}

	user, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "b@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the user that is still stored", user)
	}
}

func TestOpeningRefusesToReplaceALostCredentialKey(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if err := store.AddUser("plinkr@nauta.com.cu", "s3cret"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	store.Close()

	if err := os.Remove(filepath.Join(dir, "credentials.key")); err != nil {
		t.Fatalf("remove key: %v", err)
	}

	reopened, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt with a lost key: %v", err)
	}
	defer reopened.Close()

	if _, err := os.Stat(filepath.Join(dir, "credentials.key")); !os.IsNotExist(err) {
		t.Fatal("opening the store replaced the lost credential key on its own")
	}
	if _, _, err := reopened.FindCredentials("plinkr", ""); err == nil || !strings.Contains(err.Error(), "set-password") {
		t.Errorf("FindCredentials error = %v, want it to say how to rewrite the passwords", err)
	}
	if err := reopened.AddUser("pepe@nauta.com.cu", "pass"); err == nil {
		t.Error("AddUser created a new key on its own over passwords sealed with the lost one")
	}
}

func TestSetPasswordRecoversFromALostCredentialKey(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if err := store.AddUser("plinkr@nauta.com.cu", "s3cret"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	store.Close()
	if err := os.Remove(filepath.Join(dir, "credentials.key")); err != nil {
		t.Fatalf("remove key: %v", err)
	}

	reopened, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer reopened.Close()
	if err := reopened.SetPassword("plinkr@nauta.com.cu", "n3w"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	_, password, err := reopened.FindCredentials("plinkr", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if password != "n3w" {
		t.Errorf("password = %q, want the one set after the key was lost", password)
	}
}

func TestListUsersKeepsInsertionOrder(t *testing.T) {
	store := newStore(t)
	for _, user := range []string{"c@nauta.com.cu", "a@nauta.com.cu", "b@nauta.com.cu"} {
		if err := store.AddUser(user, "pass"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}

	users, err := store.ListUsers()
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	want := []string{"c@nauta.com.cu", "a@nauta.com.cu", "b@nauta.com.cu"}
	if len(users) != len(want) {
		t.Fatalf("ListUsers = %v, want %v", users, want)
	}
	for i := range want {
		if users[i] != want[i] {
			t.Fatalf("ListUsers = %v, want %v", users, want)
		}
	}
}

func TestUsersAreNotStoredInClearText(t *testing.T) {
	store := newStore(t)
	if err := store.AddUser("plinkr@nauta.com.cu", "hunter2"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	var stored string
	if err := store.users.QueryRow("SELECT password FROM users").Scan(&stored); err != nil {
		t.Fatalf("read stored password: %v", err)
	}
	if stored == "hunter2" {
		t.Error("the password was stored in clear text")
	}
}

func TestPasswordsWithTheSameValueAreNotEqualInTheDatabase(t *testing.T) {
	store := newStore(t)
	for _, user := range []string{"a@nauta.com.cu", "b@nauta.com.cu"} {
		if err := store.AddUser(user, "same"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}

	rows, err := store.users.Query("SELECT password FROM users")
	if err != nil {
		t.Fatalf("read passwords: %v", err)
	}
	defer rows.Close()

	var seen []string
	for rows.Next() {
		var password string
		if err := rows.Scan(&password); err != nil {
			t.Fatalf("read passwords: %v", err)
		}
		seen = append(seen, password)
	}
	if len(seen) != 2 || seen[0] == seen[1] {
		t.Error("equal passwords were stored identically, so the value is not randomised")
	}
}

func TestSaveLoginAndLogout(t *testing.T) {
	store := newStore(t)
	start := time.Date(2026, 5, 4, 10, 0, 0, 0, time.Local)

	if err := store.SaveLogin("plinkr@nauta.com.cu", start); err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}
	connections, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 1 || connections[0].End != nil {
		t.Fatalf("Connections = %+v, want one open connection", connections)
	}

	if err := store.SaveLogout("plinkr@nauta.com.cu", start.Add(30*time.Minute)); err != nil {
		t.Fatalf("SaveLogout: %v", err)
	}
	connections, err = store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if connections[0].End == nil {
		t.Fatal("the connection is still open")
	}
	if got := connections[0].End.Sub(connections[0].Start); got != 30*time.Minute {
		t.Errorf("duration = %s, want 30m", got)
	}
}

func TestSaveLogoutClosesTheMostRecentOpenConnection(t *testing.T) {
	store := newStore(t)
	first := time.Date(2026, 5, 4, 10, 0, 0, 0, time.Local)
	second := first.Add(time.Hour)

	for _, start := range []time.Time{first, second} {
		if err := store.SaveLogin("plinkr@nauta.com.cu", start); err != nil {
			t.Fatalf("SaveLogin: %v", err)
		}
	}
	if err := store.SaveLogout("plinkr@nauta.com.cu", second.Add(time.Minute)); err != nil {
		t.Fatalf("SaveLogout: %v", err)
	}

	connections, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if connections[0].End != nil {
		t.Error("the older connection was closed instead of the newer one")
	}
	if connections[1].End == nil {
		t.Error("the most recent connection was not closed")
	}
}

func TestSaveLogoutWithoutAnOpenConnection(t *testing.T) {
	store := newStore(t)
	if err := store.SaveLogout("ghost@nauta.com.cu", time.Now()); err != nil {
		t.Fatalf("SaveLogout: %v", err)
	}
}

func TestConnectionsOfMonth(t *testing.T) {
	store := newStore(t)
	reference := time.Date(2026, 5, 15, 12, 0, 0, 0, time.Local)
	saves := map[string]time.Time{
		"this month, early":  time.Date(2026, 5, 1, 0, 0, 1, 0, time.Local),
		"this month, late":   time.Date(2026, 5, 31, 23, 59, 59, 0, time.Local),
		"previous month":     time.Date(2026, 4, 30, 23, 59, 59, 0, time.Local),
		"two months back":    time.Date(2026, 3, 15, 12, 0, 0, 0, time.Local),
		"same month next yr": time.Date(2027, 5, 15, 12, 0, 0, 0, time.Local),
	}
	for _, start := range saves {
		if err := store.SaveLogin("plinkr@nauta.com.cu", start); err != nil {
			t.Fatalf("SaveLogin: %v", err)
		}
	}

	current, err := store.ConnectionsOfMonth(reference)
	if err != nil {
		t.Fatalf("ConnectionsOfMonth: %v", err)
	}
	if len(current) != 2 {
		t.Errorf("current month returned %d rows, want 2", len(current))
	}

	previous, err := store.ConnectionsOfPreviousMonth(reference)
	if err != nil {
		t.Fatalf("ConnectionsOfPreviousMonth: %v", err)
	}
	if len(previous) != 1 {
		t.Errorf("previous month returned %d rows, want 1", len(previous))
	}
	if !previous[0].Start.Equal(saves["previous month"]) {
		t.Errorf("previous month returned %s", previous[0].Start)
	}
}

func TestConnectionsOfPreviousMonthAcrossTheYearBoundary(t *testing.T) {
	store := newStore(t)
	if err := store.SaveLogin("plinkr@nauta.com.cu", time.Date(2025, 12, 20, 10, 0, 0, 0, time.Local)); err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}
	if err := store.SaveLogin("plinkr@nauta.com.cu", time.Date(2026, 1, 5, 10, 0, 0, 0, time.Local)); err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}

	previous, err := store.ConnectionsOfPreviousMonth(time.Date(2026, 1, 10, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("ConnectionsOfPreviousMonth: %v", err)
	}
	if len(previous) != 1 {
		t.Fatalf("previous month returned %d rows, want 1", len(previous))
	}
	if previous[0].Start.Month() != time.December || previous[0].Start.Year() != 2025 {
		t.Errorf("previous month returned %s, want December 2025", previous[0].Start)
	}
}

func TestConnectionsAreOrderedByStart(t *testing.T) {
	store := newStore(t)
	for _, day := range []int{20, 5, 12} {
		if err := store.SaveLogin("plinkr@nauta.com.cu", time.Date(2026, 5, day, 10, 0, 0, 0, time.Local)); err != nil {
			t.Fatalf("SaveLogin: %v", err)
		}
	}

	connections, err := store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	for i := 1; i < len(connections); i++ {
		if connections[i].Start.Before(connections[i-1].Start) {
			t.Fatalf("connections are not ordered: %s before %s",
				connections[i].Start, connections[i-1].Start)
		}
	}
}

func TestStoreKeepsFilesPrivate(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()

	for _, name := range []string{"users.db", "connections.db", "credentials.key"} {
		if err := checkMode(filepath.Join(dir, name)); err != nil {
			t.Error(err)
		}
	}
}

func checkMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		return fmt.Errorf("%s has permissions %o, want %o", path, perm, fileMode)
	}
	return nil
}

func TestDefaultUserResolvesAPrefix(t *testing.T) {
	store := newStore(t)
	for _, user := range []string{"plinkr@nauta.com.cu", "pepe@nauta.com.cu"} {
		if err := store.AddUser(user, "pass"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}
	if err := store.SetDefaultUser("pepe"); err != nil {
		t.Fatalf("SetDefaultUser: %v", err)
	}
	user, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "pepe@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the account the prefix names", user)
	}
}

func TestDefaultUserResolvesAStoredPrefix(t *testing.T) {
	// Una base heredada puede tener el predeterminado guardado como prefijo.
	store := newStore(t)
	for _, user := range []string{"plinkr@nauta.com.cu", "pepe@nauta.com.cu"} {
		if err := store.AddUser(user, "pass"); err != nil {
			t.Fatalf("AddUser: %v", err)
		}
	}
	if _, err := store.users.Exec("INSERT INTO default_user VALUES (?)", "pepe"); err != nil {
		t.Fatalf("store prefix default: %v", err)
	}
	user, err := store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if user != "pepe@nauta.com.cu" {
		t.Errorf("DefaultUser = %q, want the account the stored prefix names", user)
	}
}

func TestSetPasswordForAMissingUserKeepsTheLostKeyState(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	if err := store.AddUser("plinkr@nauta.com.cu", "s3cret"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}
	store.Close()
	keyPath := filepath.Join(dir, "credentials.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key: %v", err)
	}

	reopened, err := OpenAt(dir)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer reopened.Close()
	if err := reopened.SetPassword("plinkr", "n3w"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Error("a set-password that updated nothing created a new credential key")
	}
	if _, _, err := reopened.FindCredentials("plinkr", ""); err == nil || !strings.Contains(err.Error(), "set-password") {
		t.Errorf("FindCredentials error = %v, want the lost-key guidance to remain", err)
	}
}

func TestADanglingKeySymlinkIsAnErrorNotACrash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.key")
	if err := os.Symlink(filepath.Join(dir, "missing", "key"), path); err != nil {
		t.Skipf("symlinks not available: %v", err)
	}
	if _, err := openKeyring(path); err == nil {
		t.Error("openKeyring accepted a key path it cannot read")
	}
}
