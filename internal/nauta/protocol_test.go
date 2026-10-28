package nauta

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

var checkHosts = []string{"www.cubadebate.cu", "example.com"}

type portal struct {
	t *testing.T

	server     *httptest.Server
	probe      *httptest.Server
	pageServed string
	// onlinePage reemplaza la página que sirve online.do, para simular una
	// respuesta autenticada distinta de la habitual.
	onlinePage string
	status     int
	online     bool

	// deadChecks son los destinos del sondeo que no se alcanzan, como los que no
	// salen de un VPN. Se llenan antes de empezar, así que no hay carrera.
	deadChecks map[string]bool
	probeHits  atomic.Int64

	logins  []url.Values
	logouts []string
	queries []url.Values
	landing []url.Values
}

func newPortal(t *testing.T) *portal {
	t.Helper()
	p := &portal{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			serveLanding(p, w)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse landing form: %v", err)
		}
		p.landing = append(p.landing, r.Form)
		serveLoginPage(p, w)
	})

	mux.HandleFunc("/LoginServlet", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse login form: %v", err)
		}
		p.logins = append(p.logins, r.Form)
		if p.status != 0 {
			w.WriteHeader(p.status)
		}
		if p.pageServed == "" {
			p.pageServed = "logged_in.html"
		}
		if p.pageServed == "logged_in.html" {
			http.Redirect(w, r, "/online.do", http.StatusSeeOther)
			return
		}
		serveFixture(p, w, p.pageServed)
	})

	mux.HandleFunc("/online.do", func(w http.ResponseWriter, _ *http.Request) {
		if p.onlinePage != "" {
			serveFixture(p, w, p.onlinePage)
			return
		}
		serveFixture(p, w, "logged_in.html")
	})

	mux.HandleFunc("/LogoutServlet", func(w http.ResponseWriter, r *http.Request) {
		p.logouts = append(p.logouts, r.URL.RawQuery)
		serveFixture(p, w, "logout_success.html")
	})

	mux.HandleFunc("/EtecsaQueryServlet", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse query form: %v", err)
		}
		p.queries = append(p.queries, r.Form)
		if r.Form.Get("op") == "getLeftTime" {
			reply(w, "02:14:24")
			return
		}
		serveFixture(p, w, "session_info.html")
	})

	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		p.probeHits.Add(1)
		if p.online {
			reply(w, "the real site")
			return
		}
		reply(w, "<html><body>secure.etecsa.net</body></html>")
	})
	p.probe = httptest.NewServer(probeMux)
	t.Cleanup(p.probe.Close)

	// TLS para conservar el esquema y host de producción: el protocolo distingue
	// la intercepción por la URL de la respuesta.
	p.server = httptest.NewTLSServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

func serveLanding(p *portal, w http.ResponseWriter) {
	serveFixture(p, w, "landing.html")
}

func serveLoginPage(p *portal, w http.ResponseWriter) {
	serveFixture(p, w, "login_page.html")
}

func serveFixture(p *portal, w http.ResponseWriter, name string) {
	serveTestFixture(p.t, w, name)
}

func serveTestFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	reply(w, string(body))
}

func reply(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}

func TestIsConnectedAsksThePortalNothingWhenItIntercepts(t *testing.T) {
	portal := newPortal(t)
	protocol := NewProtocol(portal.client(t))

	if protocol.IsConnected(context.Background()) {
		t.Error("IsConnected = true while the portal intercepts")
	}
	// El portal intercepta todos los destinos: probar otro solo añade espera.
	if hits := portal.probeHits.Load(); hits != 1 {
		t.Errorf("probe hits = %d, want a single check", hits)
	}
}

func TestIsConnectedFallsBackToAnotherTarget(t *testing.T) {
	portal := newPortal(t)
	portal.online = true
	portal.deadChecks = map[string]bool{checkHosts[0]: true}
	protocol := NewProtocol(portal.client(t))

	if !protocol.IsConnected(context.Background()) {
		t.Error("IsConnected = false, want the second target to answer")
	}
	if hits := portal.probeHits.Load(); hits != 1 {
		t.Errorf("probe hits = %d, want the dead target to be skipped", hits)
	}
}

func TestIsConnectedWithoutAnyTarget(t *testing.T) {
	portal := newPortal(t)
	portal.online = true
	portal.deadChecks = map[string]bool{checkHosts[0]: true, checkHosts[1]: true}
	protocol := NewProtocol(portal.client(t))

	if protocol.IsConnected(context.Background()) {
		t.Error("IsConnected = true with no reachable target")
	}
}

func TestStartSessionReadsTheLoginForm(t *testing.T) {
	portal := newPortal(t)
	protocol := NewProtocol(nil)
	protocol.client = portal.client(t)

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if session.CSRFHW != "1fe3ee0634195096337177a0994723fb" {
		t.Errorf("CSRFHW = %q", session.CSRFHW)
	}
	if session.WlanUserIP != "10.190.20.96" {
		t.Errorf("WlanUserIP = %q", session.WlanUserIP)
	}
	if !strings.HasSuffix(session.LoginAction, "/LoginServlet") {
		t.Errorf("LoginAction = %q, want the login servlet", session.LoginAction)
	}
	if len(portal.landing) != 1 {
		t.Fatalf("landing posted %d times, want 1", len(portal.landing))
	}
	if got := portal.landing[0].Get("wlanuserip"); got != "10.190.20.96" {
		t.Errorf("landing wlanuserip = %q", got)
	}
	if got := portal.landing[0].Get("wlanparameter"); got == "" {
		t.Error("landing did not carry the wlanparameter the entry page provides")
	}
}

func TestAuthenticateStoresTheSessionToken(t *testing.T) {
	portal := newPortal(t)
	protocol := NewProtocol(portal.client(t))

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := protocol.Authenticate(context.Background(), session, "plinkr@nauta.com.cu", "s3cret"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	if session.AttributeUUID != "B2F6AAB9A9868BABC0BDC6B7A235ABE2" {
		t.Errorf("AttributeUUID = %q", session.AttributeUUID)
	}
	if len(portal.logins) != 1 {
		t.Fatalf("login posted %d times, want 1", len(portal.logins))
	}
	form := portal.logins[0]
	for key, want := range map[string]string{
		"CSRFHW":     "1fe3ee0634195096337177a0994723fb",
		"wlanuserip": "10.190.20.96",
		"username":   "plinkr@nauta.com.cu",
		"password":   "s3cret",
	} {
		if got := form.Get(key); got != want {
			t.Errorf("login %s = %q, want %q", key, got, want)
		}
	}
}

func TestAuthenticateReportsThePortalReason(t *testing.T) {
	portal := newPortal(t)
	portal.pageServed = "failed_login.html"
	protocol := NewProtocol(portal.client(t))

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	err = protocol.Authenticate(context.Background(), session, "plinkr@nauta.com.cu", "wrong")
	if err == nil {
		t.Fatal("Authenticate accepted a rejected login")
	}
	if !strings.Contains(err.Error(), "El usuario o la contraseña son incorrectos") {
		t.Errorf("error = %q, want the reason the portal gave", err)
	}
	if session.AttributeUUID != "" {
		t.Error("a rejected login stored a session token")
	}
}

func TestAuthenticateReportsTheStatus(t *testing.T) {
	portal := newPortal(t)
	portal.pageServed = "failed_login.html"
	portal.status = http.StatusUnauthorized
	protocol := NewProtocol(portal.client(t))

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	err = protocol.Authenticate(context.Background(), session, "plinkr@nauta.com.cu", "wrong")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want it to mention the 401", err)
	}
}

func TestAuthenticateRejectsAnAnswerOutsideTheOnlinePage(t *testing.T) {
	portal := newPortal(t)
	portal.pageServed = "session_info.html"
	protocol := NewProtocol(portal.client(t))

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := protocol.Authenticate(context.Background(), session, "user", "pass"); err == nil {
		t.Error("Authenticate accepted an answer that never reached online.do")
	}
}

func TestAuthenticateAcceptsTheOnlinePageWithoutAToken(t *testing.T) {
	portal := newPortal(t)
	portal.onlinePage = "session_info.html"
	protocol := NewProtocol(portal.client(t))

	session, err := protocol.StartSession(context.Background())
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if err := protocol.Authenticate(context.Background(), session, "user", "pass"); err != nil {
		t.Fatalf("Authenticate rejected a login the portal accepted: %v", err)
	}
	if session.AttributeUUID != "" {
		t.Errorf("AttributeUUID = %q, want empty when the portal sent none", session.AttributeUUID)
	}
}

func TestStartSessionFailsWithoutALoginForm(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "<html><body>no form here</body></html>")
	}))
	defer server.Close()

	protocol := NewProtocol(rewriteClient(t, server))
	if _, err := protocol.StartSession(context.Background()); err == nil {
		t.Error("StartSession accepted a page without the login form")
	}
}

func TestLogoutCarriesTheSessionTokens(t *testing.T) {
	portal := newPortal(t)
	protocol := NewProtocol(portal.client(t))

	session := &Session{
		CSRFHW:        "1fe3ee0634195096337177a0994723fb",
		WlanUserIP:    "10.190.20.96",
		AttributeUUID: "B2F6AAB9A9868BABC0BDC6B7A235ABE2",
	}
	if err := protocol.Logout(context.Background(), session, "plinkr@nauta.com.cu"); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if len(portal.logouts) != 1 {
		t.Fatalf("logout posted %d times, want 1", len(portal.logouts))
	}
	query, err := url.ParseQuery(portal.logouts[0])
	if err != nil {
		t.Fatalf("parse logout query: %v", err)
	}
	for key, want := range map[string]string{
		"CSRFHW":         "1fe3ee0634195096337177a0994723fb",
		"ATTRIBUTE_UUID": "B2F6AAB9A9868BABC0BDC6B7A235ABE2",
		"wlanuserip":     "10.190.20.96",
		"username":       "plinkr@nauta.com.cu",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("logout %s = %q, want %q", key, got, want)
		}
	}
}

func TestLogoutReportsARejectedLogout(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serveTestFixture(t, w, "logout_failure.html")
	}))
	defer server.Close()

	protocol := NewProtocol(rewriteClient(t, server))
	err := protocol.Logout(context.Background(), &Session{}, "plinkr@nauta.com.cu")
	if err == nil {
		t.Fatal("Logout accepted an answer without a success token")
	}
	if !strings.Contains(err.Error(), "Fallo al cerrar la sesión") {
		t.Errorf("error = %q", err)
	}
}

func TestRemainingTimeAsksTheQueryServlet(t *testing.T) {
	portal := newPortal(t)
	protocol := NewProtocol(portal.client(t))

	session := &Session{
		CSRFHW:        "1fe3ee0634195096337177a0994723fb",
		WlanUserIP:    "10.190.20.96",
		AttributeUUID: "B2F6AAB9A9868BABC0BDC6B7A235ABE2",
	}
	value, err := protocol.RemainingTime(context.Background(), session, "plinkr@nauta.com.cu")
	if err != nil {
		t.Fatalf("RemainingTime: %v", err)
	}
	if value != "02:14:24" {
		t.Errorf("remaining time = %q", value)
	}
	if len(portal.queries) != 1 || portal.queries[0].Get("op") != "getLeftTime" {
		t.Fatalf("query = %v, want a getLeftTime request", portal.queries)
	}
}

func TestUserCreditReadsTheAccountTable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		serveTestFixture(t, w, "session_info.html")
	}))
	defer server.Close()

	protocol := NewProtocol(rewriteClient(t, server))
	session := &Session{CSRFHW: "abc", WlanUserIP: "10.190.20.96"}
	credit, err := protocol.UserCredit(context.Background(), session, "plinkr@nauta.com.cu", "s3cret")
	if err != nil {
		t.Fatalf("UserCredit: %v", err)
	}
	if credit != "1.12 CUC" {
		t.Errorf("credit = %q, want %q", credit, "1.12 CUC")
	}
}

func TestUserCreditRefusesAnInterceptedRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/EtecsaQueryServlet", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://10.190.20.96:8443/LoginServlet", http.StatusFound)
	})
	mux.HandleFunc("/LoginServlet", func(w http.ResponseWriter, _ *http.Request) {
		serveTestFixture(t, w, "failed_login.html")
	})
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	protocol := NewProtocol(rewriteClient(t, server))
	session := &Session{CSRFHW: "abc", WlanUserIP: "10.190.20.96"}
	_, err := protocol.UserCredit(context.Background(), session, "user", "pass")
	if err == nil {
		t.Fatal("UserCredit answered an intercepted request")
	}
	if !strings.Contains(err.Error(), "mientras está online") {
		t.Errorf("error = %q", err)
	}
}

func TestUserCreditReportsAMissingTable(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "<html><body>nothing to report</body></html>")
	}))
	defer server.Close()

	protocol := NewProtocol(rewriteClient(t, server))
	_, err := protocol.UserCredit(context.Background(), &Session{}, "user", "pass")
	if err == nil || !strings.Contains(err.Error(), "no se encontró la información") {
		t.Fatalf("error = %v, want a missing information error", err)
	}
}

// Solo redirige el dial: las capturas reales llevan la acción de producción en
// sus formularios y deben seguir funcionando sin modificarse.
func (p *portal) client(t *testing.T) *http.Client {
	t.Helper()
	dialer := &net.Dialer{Timeout: DefaultTimeout}
	portal := p.server.Listener.Addr().String()
	probe := p.probe.Listener.Addr().String()

	return &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(address)
				if !slices.Contains(checkHosts, host) {
					return dialer.DialContext(ctx, network, portal)
				}
				if p.deadChecks[host] {
					return nil, fmt.Errorf("sin ruta a %s", host)
				}
				return dialer.DialContext(ctx, network, probe)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func rewriteClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	address := server.Listener.Addr().String()
	dialer := &net.Dialer{Timeout: DefaultTimeout}
	return &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, address)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}
