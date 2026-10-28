package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
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
	"time"

	"github.com/plinkr/nautago/internal/nauta"
	"github.com/plinkr/nautago/internal/storage"
)

type testApp struct {
	*App
	dir string

	out *bytes.Buffer
	err *bytes.Buffer
}

func newTestApp(t *testing.T) *testApp {
	t.Helper()
	dir := t.TempDir()
	store, err := storage.OpenAt(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &testApp{
		App: &App{
			Out:     out,
			Err:     errOut,
			In:      strings.NewReader(""),
			Store:   store,
			Now:     time.Now,
			Tunnels: noTunnels(),
		},
		dir: dir,
		out: out,
		err: errOut,
	}
}

func noTunnels() Tunnels {
	return Tunnels{
		OpenVPNRunning:   func() bool { return false },
		StopOpenVPN:      func(context.Context) error { return nil },
		ProtonVPNRunning: func(context.Context) bool { return false },
		StopProtonVPN:    func(context.Context) error { return nil },
	}
}

// tunnelsUp finge un cliente VPN conectado. up es el estado que la prueba mueve:
// parar el túnel lo baja, como hace el cierre real.
func tunnelsUp(up *atomic.Bool) Tunnels {
	return Tunnels{
		OpenVPNRunning:   func() bool { return up.Load() },
		StopOpenVPN:      func(context.Context) error { up.Store(false); return nil },
		ProtonVPNRunning: func(context.Context) bool { return false },
		StopProtonVPN:    func(context.Context) error { return nil },
	}
}

func (a *testApp) run(args ...string) int {
	return a.Run(context.Background(), args)
}

func (a *testApp) sessionPath() string { return filepath.Join(a.dir, sessionFileName) }

// Usa nauta.SaveSession y no un JSON escrito a mano para que la prueba siga el
// formato real si cambia.
func (a *testApp) writeSession(t *testing.T) {
	t.Helper()
	session := &nauta.Session{
		LoginAction:   "https://secure.etecsa.net:8443/LoginServlet",
		CSRFHW:        "1fe3ee0634195096337177a0994723fb",
		WlanUserIP:    "10.190.20.96",
		AttributeUUID: "an-attribute-uuid",
		Username:      "plinkr@nauta.com.cu",
	}
	if err := nauta.SaveSession(a.sessionPath(), session); err != nil {
		t.Fatalf("save session: %v", err)
	}
}

type fakePortal struct {
	server *httptest.Server
	probe  *httptest.Server

	online        bool
	pageServed    string
	remainingTime string
	credit        string

	// probeDead deja los destinos del sondeo sin ruta, como una red a la que el
	// VPN no deja salir.
	probeDead bool
	// tunnelUp: con el túnel arriba el portal no responde. Se llama desde la
	// goroutine del servidor, así que debe ser seguro para concurrencia.
	tunnelUp func() bool
	// afterQuery se dispara con cada consulta a la cuenta, para modelar un VPN que
	// se conecta a mitad de sesión.
	afterQuery func()

	logouts      []url.Values
	queriedUsers []string
}

func newFakePortal(t *testing.T) *fakePortal {
	t.Helper()
	p := &fakePortal{pageServed: "logged_in.html", remainingTime: "02:14:24"}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeFixture(t, w, "landing.html")
			return
		}
		writeFixture(t, w, "login_page.html")
	})
	mux.HandleFunc("/LoginServlet", func(w http.ResponseWriter, r *http.Request) {
		if p.pageServed == "logged_in.html" {
			http.Redirect(w, r, "/online.do", http.StatusSeeOther)
			return
		}
		writeFixture(t, w, p.pageServed)
	})
	mux.HandleFunc("/online.do", func(w http.ResponseWriter, _ *http.Request) {
		writeFixture(t, w, "logged_in.html")
	})
	mux.HandleFunc("/LogoutServlet", func(w http.ResponseWriter, r *http.Request) {
		if p.tunneled() {
			http.Error(w, "el tunel se queda con la ruta", http.StatusBadGateway)
			return
		}
		// Una salida que respondiera SUCCESS sin los tokens escondería una CLI que
		// cerró una sesión distinta de la que recibió.
		p.logouts = append(p.logouts, r.URL.Query())
		if r.URL.Query().Get("ATTRIBUTE_UUID") == "" || r.URL.Query().Get("CSRFHW") == "" {
			http.Error(w, "missing session tokens", http.StatusBadRequest)
			return
		}
		reply(w, "LOGOUT SUCCESS")
	})
	mux.HandleFunc("/EtecsaQueryServlet", func(w http.ResponseWriter, r *http.Request) {
		if p.tunneled() {
			http.Error(w, "el tunel se queda con la ruta", http.StatusBadGateway)
			return
		}
		defer func() {
			if p.afterQuery != nil {
				p.afterQuery()
			}
		}()
		p.queriedUsers = append(p.queriedUsers, r.FormValue("username"))
		if r.FormValue("op") == "getLeftTime" {
			reply(w, p.remainingTime)
			return
		}
		if p.credit == "" {
			reply(w, "<html><body>no account here</body></html>")
			return
		}
		reply(w, fmt.Sprintf(sessionInfo, p.credit))
	})

	probeMux := http.NewServeMux()
	probeMux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		if p.online {
			reply(w, "the real site")
			return
		}
		reply(w, "<html><body>secure.etecsa.net</body></html>")
	})

	p.probe = httptest.NewServer(probeMux)
	t.Cleanup(p.probe.Close)
	p.server = httptest.NewTLSServer(mux)
	t.Cleanup(p.server.Close)
	return p
}

var checkHosts = []string{"www.cubadebate.cu", "example.com"}

func (p *fakePortal) tunneled() bool { return p.tunnelUp != nil && p.tunnelUp() }

// Solo redirige el dial: las URL siguen siendo las de producción, así que la CLI
// recorre los mismos caminos que en uso real.
func (p *fakePortal) client() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	portal := p.server.Listener.Addr().String()
	probe := p.probe.Listener.Addr().String()

	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
				host, _, _ := net.SplitHostPort(address)
				if !slices.Contains(checkHosts, host) {
					return dialer.DialContext(ctx, network, portal)
				}
				if p.probeDead {
					return nil, errors.New("sin ruta a " + host)
				}
				return dialer.DialContext(ctx, network, probe)
			},
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
}

func reply(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}

func writeFixture(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "nauta", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	reply(w, string(data))
}

func (a *testApp) withPortal(p *fakePortal) *testApp {
	a.HTTPClient = p.client()
	return a
}

func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

const sessionInfo = `<html><body><table id="sessioninfo"><tbody>
<tr><td class="key">Usuario</td><td>plinkr@nauta.com.cu</td></tr>
<tr><td class="key">Saldo</td><td>%s</td></tr>
</tbody></table></body></html>`
