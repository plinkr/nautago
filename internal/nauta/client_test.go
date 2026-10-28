package nauta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeLog struct {
	mu      sync.Mutex
	logins  []string
	logouts []string
	err     error
}

func (l *fakeLog) SaveLogin(user string, _ time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.logins = append(l.logins, user)
	return nil
}

func (l *fakeLog) SaveLogout(user string, _ time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != nil {
		return l.err
	}
	l.logouts = append(l.logouts, user)
	return nil
}

func newTestClient(t *testing.T, user, password string, p *portal, log ConnectionLog) (*Client, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "nauta-session")
	cfg := Config{SessionPath: path, Log: log, RetryDelay: time.Millisecond}
	if p != nil {
		cfg.HTTPClient = p.client(t)
	}
	return NewClient(user, password, cfg), path
}

func serving(t *testing.T, server *httptest.Server) *portal {
	t.Helper()
	return &portal{t: t, server: server, probe: server}
}

func TestLoginPersistsTheSession(t *testing.T) {
	portal := newPortal(t)
	log := &fakeLog{}
	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, log)

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !client.IsLoggedIn() {
		t.Error("IsLoggedIn = false after a login")
	}

	stored, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if stored.Username != "plinkr@nauta.com.cu" {
		t.Errorf("stored user = %q", stored.Username)
	}
	if stored.AttributeUUID == "" || stored.CSRFHW == "" || stored.WlanUserIP == "" {
		t.Errorf("stored session is incomplete: %+v", stored)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session permissions = %o, want 600", perm)
	}
}

func TestLoginRefusesAnExistingSessionOnAnOpenConnection(t *testing.T) {
	portal := newPortal(t)
	portal.online = true

	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, nil)
	if err := SaveSession(path, &Session{Username: "plinkr@nauta.com.cu"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	err := client.Login(context.Background())
	if !errors.Is(err, ErrAlreadyLoggedIn) {
		t.Fatalf("Login error = %v, want ErrAlreadyLoggedIn", err)
	}
}

func TestLoginReplacesAStaleSessionWhileIntercepted(t *testing.T) {
	portal := newPortal(t)
	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, nil)
	if err := SaveSession(path, &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "stale"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}

	stored, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if stored.CSRFHW == "stale" {
		t.Error("the stale session was kept instead of being replaced")
	}
}

func TestLoginRefusesAnOpenConnection(t *testing.T) {
	portal := newPortal(t)
	portal.online = true

	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, nil)
	err := client.Login(context.Background())
	if !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("Login error = %v, want ErrAlreadyConnected", err)
	}
	if len(portal.logins) != 0 {
		t.Errorf("the portal saw %d logins, want none", len(portal.logins))
	}
}

func TestLogoutClearsTheSessionAndRecordsTheTime(t *testing.T) {
	portal := newPortal(t)
	log := &fakeLog{}
	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, log)

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if client.IsLoggedIn() {
		t.Error("the session survived the logout")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the session file survived the logout")
	}
	if len(portal.logouts) != 1 {
		t.Errorf("portal saw %d logouts, want 1", len(portal.logouts))
	}
	if len(log.logouts) != 1 || log.logouts[0] != "plinkr@nauta.com.cu" {
		t.Errorf("recorded logouts = %v", log.logouts)
	}
}

func TestLogoutRecordsTheTimeEvenWhenItFails(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/LogoutServlet", func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "nothing to close")
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	log := &fakeLog{}
	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), log)
	if err := SaveSession(filepath.Join(t.TempDir(), "unused"), &Session{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	client.session = &Session{Username: "plinkr@nauta.com.cu"}

	if err := client.Logout(context.Background()); err == nil {
		t.Fatal("Logout reported success for a rejected logout")
	}
	if len(log.logouts) != 1 {
		t.Errorf("recorded logouts = %v, want one entry", log.logouts)
	}
}

func TestLogoutReportsAHistoryFailureWithoutFailing(t *testing.T) {
	portal := newPortal(t)
	log := &fakeLog{err: errors.New("disco lleno")}
	var reported []error
	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, log)
	client.cfg.OnHistoryError = func(err error) { reported = append(reported, err) }

	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if len(reported) == 0 {
		t.Error("the history failure was swallowed instead of reported")
	}
	if client.IsLoggedIn() {
		t.Error("the session was not disposed even though the disconnect worked")
	}
}

func TestAnUnusableSessionFileDoesNotBlockDisconnection(t *testing.T) {
	portal := newPortal(t)
	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", portal, nil)
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("truncate session: %v", err)
	}

	if client.IsLoggedIn() {
		t.Error("an unreadable session file was reported as an active session")
	}
	if err := client.Login(context.Background()); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !client.IsLoggedIn() {
		t.Error("the fresh login did not replace the unreadable session file")
	}
}

func TestLogoutRetriesTransportFailures(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		count := attempts
		mu.Unlock()
		if count < 3 {
			hijackAndDrop(t, w)
			return
		}
		reply(w, "LOGOUT SUCCESS")
	}))
	defer server.Close()

	log := &fakeLog{}
	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), log)
	client.session = &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "abc"}

	if err := client.Logout(context.Background()); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestLogoutGivesUpAfterTheLastAttempt(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		hijackAndDrop(t, w)
	}))
	defer server.Close()

	log := &fakeLog{}
	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), log)
	client.session = &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "abc"}

	err := client.Logout(context.Background())
	if err == nil {
		t.Fatal("Logout reported success although the portal never answered")
	}
	if attempts != maxDisconnectAttempts {
		t.Errorf("attempts = %d, want %d", attempts, maxDisconnectAttempts)
	}
	if !strings.Contains(err.Error(), "Hay problemas en la red") {
		t.Errorf("error = %q, want the network failure message", err)
	}
}

func TestLogoutDoesNotRetryAPortalRejection(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		reply(w, "nothing to close")
	}))
	defer server.Close()

	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), nil)
	client.session = &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "abc"}

	if err := client.Logout(context.Background()); err == nil {
		t.Fatal("Logout reported success for a rejected logout")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1: a rejection is not a transport failure", attempts)
	}
}

func TestLogoutWithoutASession(t *testing.T) {
	client, _ := newTestClient(t, "user", "pass", nil, nil)
	if err := client.Logout(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Logout error = %v, want ErrNoSession", err)
	}
}

func TestLogoutKeepsTheTokensWhenThePortalDoesNotAnswer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijackAndDrop(t, w)
	}))
	defer server.Close()

	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), nil)
	client.session = &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "abc", AttributeUUID: "token"}
	if err := SaveSession(path, client.session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	if err := client.Logout(context.Background()); err == nil {
		t.Fatal("Logout reported success although the portal never answered")
	}
	if !client.IsLoggedIn() {
		t.Error("the session was given up after a failed disconnect, so the retry is impossible")
	}
	if client.session == nil {
		t.Error("the client dropped the session it still has to close")
	}
}

func TestLogoutDoesNotLeakTheSessionTokens(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hijackAndDrop(t, w)
	}))
	defer server.Close()

	client, _ := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", serving(t, server), nil)
	client.session = &Session{
		Username:      "plinkr@nauta.com.cu",
		CSRFHW:        "1fe3ee0634195096337177a0994723fb",
		WlanUserIP:    "10.190.20.96",
		AttributeUUID: "an-attribute-uuid",
	}

	err := client.Logout(context.Background())
	if err == nil {
		t.Fatal("Logout reported success although the portal never answered")
	}
	message := err.Error()
	for _, secret := range []string{
		"an-attribute-uuid",
		"1fe3ee0634195096337177a0994723fb",
		"10.190.20.96",
		"ATTRIBUTE_UUID",
		"CSRFHW",
	} {
		if strings.Contains(message, secret) {
			t.Errorf("the logout failure message carries %q: %s", secret, message)
		}
	}
}

func TestLoadSessionAdoptsTheStoredUser(t *testing.T) {
	client, path := newTestClient(t, "", "", nil, nil)
	if err := SaveSession(path, &Session{Username: "plinkr@nauta.com.cu", CSRFHW: "abc"}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := client.LoadSession(); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if client.User() != "plinkr@nauta.com.cu" {
		t.Errorf("User = %q, want the stored user", client.User())
	}
}

func TestRecordLoginOnlyTouchesTheHistory(t *testing.T) {
	log := &fakeLog{}
	client, path := newTestClient(t, "plinkr@nauta.com.cu", "s3cret", nil, log)

	if err := client.RecordLogin(); err != nil {
		t.Fatalf("RecordLogin: %v", err)
	}
	if len(log.logins) != 1 {
		t.Errorf("recorded logins = %v", log.logins)
	}
	if client.IsLoggedIn() {
		t.Error("recording a login created a session")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("recording a login wrote the session file")
	}
}

func TestSessionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nauta-session")
	want := &Session{
		LoginAction:   "https://secure.etecsa.net:8443//LoginServlet",
		CSRFHW:        "csrf",
		WlanUserIP:    "10.190.20.96",
		AttributeUUID: "uuid",
		Username:      "plinkr@nauta.com.cu",
	}
	if err := SaveSession(path, want); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	got, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if *got != *want {
		t.Errorf("round trip = %+v, want %+v", *got, *want)
	}
}

func TestLoadMissingSession(t *testing.T) {
	_, err := LoadSession(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("error = %v, want ErrNoSession", err)
	}
}

func TestDisposeSessionIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nauta-session")
	if err := SaveSession(path, &Session{}); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := DisposeSession(path); err != nil {
		t.Fatalf("DisposeSession: %v", err)
	}
	if err := DisposeSession(path); err != nil {
		t.Fatalf("second DisposeSession: %v", err)
	}
}

func hijackAndDrop(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Errorf("hijack: %v", err)
		return
	}
	conn.Close()
}
