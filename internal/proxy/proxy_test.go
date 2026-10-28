package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type countingSession struct {
	mu sync.Mutex

	existing bool
	opens    int
	closes   int
	openErr  error
}

// Imita un archivo de sesión real: existe si algo lo abrió y nadie lo cerró, o
// si ya existía antes de arrancar el proxy.
func (s *countingSession) Exists() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.existing || s.opens > s.closes
}

func (s *countingSession) Open(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	return s.openErr
}

func (s *countingSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return nil
}

func (s *countingSession) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens, s.closes
}

func newTestProxy(t *testing.T, session Session, mutate func(*Options)) *Proxy {
	t.Helper()
	opts := Options{
		Port:         0,
		TimeUnit:     time.Millisecond,
		CloseTimeout: time.Second,
		Session:      session,
	}
	if mutate != nil {
		mutate(&opts)
	}
	p, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func reply(w http.ResponseWriter, body string) {
	_, _ = w.Write([]byte(body))
}

func serve(t *testing.T, p *Proxy) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := p.ServeListener(ctx, listener); err != nil {
			t.Errorf("ServeListener: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the proxy did not stop")
		}
	})
	return "http://" + listener.Addr().String()
}

func proxyClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			DisableKeepAlives: true,
		},
	}
}

func throughProxy(t *testing.T, proxyAddr, target string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	return viaProxy(proxyAddr).Do(req)
}

func viaProxy(proxyAddr string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			Proxy:             http.ProxyURL(mustParse(proxyAddr)),
			DialContext:       (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
			DisableKeepAlives: true,
		},
	}
}

func mustParse(raw string) *url.URL {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the condition was not met before the deadline")
}

func TestNewRejectsIncompleteOptions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Options)
	}{
		{"no session", func(o *Options) { o.Session = nil }},
		{"no time unit", func(o *Options) { o.TimeUnit = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := Options{Port: 3128, TimeUnit: time.Second, Session: &countingSession{}}
			c.mutate(&opts)
			if _, err := New(opts); err == nil {
				t.Error("New accepted an incomplete configuration")
			}
		})
	}
}

func TestServeRejectsAnInvalidPort(t *testing.T) {
	p := newTestProxy(t, &countingSession{}, func(o *Options) { o.Port = -1 })
	if err := p.Serve(context.Background()); err == nil {
		t.Fatal("Serve accepted an invalid port")
	}
}

func TestServeListensOnLoopbackByDefault(t *testing.T) {
	port := freePort(t)
	p := newTestProxy(t, &countingSession{}, func(o *Options) { o.Port = port })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("the proxy did not stop")
		}
	})

	addr, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(loopbackHost, strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTCP("tcp", nil, addr)
		if err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the proxy is not answering on %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func TestAcquireDoesNotHoldTheGateDuringTheLogin(t *testing.T) {
	release := make(chan struct{})
	session := &blockingSession{entered: make(chan struct{}), release: release}
	g := newGate(session, time.Millisecond, time.Second, log.New(io.Discard, "", 0))

	acquired := make(chan error, 1)
	go func() {
		_, err := g.acquire(context.Background())
		acquired <- err
	}()

	<-session.entered

	// Con el login en vuelo, una liberación debe tomar el candado de inmediato.
	done := make(chan struct{})
	go func() {
		g.release()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("release blocked while a session was being opened")
	}

	close(release)
	if err := <-acquired; err != nil {
		t.Fatalf("acquire: %v", err)
	}
}

func TestAcquireGivesUpWhenTheRequestIsCancelled(t *testing.T) {
	g := newGate(&blockingSession{entered: make(chan struct{}), release: make(chan struct{})},
		time.Minute, 30*time.Second, log.New(io.Discard, "", 0))

	g.mu.Lock()
	g.closing = true
	g.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	acquired := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx)
		acquired <- err
	}()
	cancel()

	select {
	case err := <-acquired:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("acquire error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire ignored the cancelled request while a session was closing")
	}
}

type blockingSession struct {
	entered chan struct{}
	release chan struct{}
}

func (s *blockingSession) Exists() bool { return false }

func (s *blockingSession) Open(ctx context.Context) error {
	close(s.entered)
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingSession) Close(context.Context) error { return nil }

func TestProxiesAPlainRequest(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s", r.Host)
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, nil))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "hello from") {
		t.Errorf("body = %q", body)
	}
	opens, _ := session.counts()
	if opens != 1 {
		t.Errorf("session opened %d times, want 1", opens)
	}
}

func TestOpensOneSessionForConcurrentRequests(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		reply(w, "done")
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, nil))

	const requests = 5
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := throughProxy(t, addr, origin.URL)
			if err != nil {
				t.Errorf("request through the proxy: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	// El cierre es asíncrono respecto a las respuestas.
	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})
	if opens, _ := session.counts(); opens != 1 {
		t.Errorf("session opened %d times, want 1", opens)
	}
}

func TestRequestsWithinABillingUnitShareOneSession(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.TimeUnit = 10 * time.Second
	}))

	for i := 0; i < 3; i++ {
		resp, err := throughProxy(t, addr, origin.URL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}

	if opens, _ := session.counts(); opens != 1 {
		t.Errorf("session opened %d times, want 1", opens)
	}
}

func TestOpensANewSessionAfterTheBillingBoundary(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	const unit = 60 * time.Millisecond
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.TimeUnit = unit
	}))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()
	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})

	resp, err = throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	resp.Body.Close()

	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 2
	})
	if opens, _ := session.counts(); opens != 2 {
		t.Errorf("session opened %d times, want one per billing period", opens)
	}
}

func TestNeverClosesASessionItDidNotOpen(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{existing: true}
	addr := serve(t, newTestProxy(t, session, nil))

	for i := 0; i < 2; i++ {
		resp, err := throughProxy(t, addr, origin.URL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
	}

	opens, closes := session.counts()
	if opens != 0 {
		t.Errorf("the proxy opened a session that already existed (%d times)", opens)
	}
	if closes != 0 {
		t.Errorf("the proxy closed a session it did not open (%d times)", closes)
	}
}

func TestRejectsWhenThePortalRefuses(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{openErr: fmt.Errorf("credenciales incorrectas")}
	addr := serve(t, newTestProxy(t, session, nil))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "incorrectas") {
		t.Errorf("the answer leaked the portal failure: %q", body)
	}
}

func TestDomainFilter(t *testing.T) {
	cases := []struct {
		name   string
		allow  []string
		deny   []string
		host   string
		accept bool
	}{
		{"nothing configured", nil, nil, "example.com", true},
		{"allow list accepts a member", []string{"example.com"}, nil, "www.example.com", true},
		{"allow list rejects the rest", []string{"example.com"}, nil, "other.org", false},
		{"deny list rejects a member", nil, []string{"example.com"}, "www.example.com", false},
		{"deny list accepts the rest", nil, []string{"example.com"}, "other.org", true},
		{"deny wins over allow", []string{"example.com"}, []string{"api.example.com"}, "api.example.com", false},
		{"wildcard", nil, []string{"*.ads.example"}, "x.ads.example", false},
		{"suffix is not a prefix match", nil, []string{"example.com"}, "notexample.com", true},
		{"trailing dot does not evade deny", nil, []string{"example.com"}, "example.com.", false},
		{"trailing dot does not evade a deny suffix", nil, []string{"example.com"}, "www.example.com.", false},
		{"trailing dot in the pattern", nil, []string{"example.com."}, "example.com", false},
		{"trailing dot is allowed by allow", []string{"example.com"}, nil, "example.com.", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := newTestProxy(t, &countingSession{}, func(o *Options) {
				o.Allow, o.Deny = c.allow, c.deny
			})
			if got := p.allowed(c.host); got != c.accept {
				t.Errorf("allowed(%q) = %v, want %v", c.host, got, c.accept)
			}
		})
	}
}

func TestBlockedDomainIsNotProxied(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.Deny = []string{"127.0.0.1"}
	}))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
	if opens, _ := session.counts(); opens != 0 {
		t.Error("a blocked request opened a session")
	}
}

func TestMaxConnectionsIsEnforced(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.MaxConn = 1
	}))

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := throughProxy(t, addr, origin.URL)
		if err != nil {
			t.Errorf("first request: %v", err)
			return
		}
		resp.Body.Close()
	}()
	time.Sleep(100 * time.Millisecond)

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}

	close(release)
	wg.Wait()
}

func TestTunnelsConnect(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if _, err := http.ReadRequest(bufio.NewReader(conn)); err != nil {
					return
				}
				_, _ = io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\n\r\ntunn")
			}()
		}
	}()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, nil))

	conn, err := net.DialTimeout("tcp", mustParse(addr).Host, 2*time.Second)
	if err != nil {
		t.Fatalf("dial the proxy: %v", err)
	}
	defer conn.Close()

	target := listener.Addr().String()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read the tunnel status: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("tunnel status = %q, want 200", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read the tunnel headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", target)
	body, err := io.ReadAll(reader)
	if err != nil && !strings.Contains(string(body), "tunn") {
		t.Fatalf("read the tunnelled answer: %v", err)
	}
	if !strings.Contains(string(body), "tunn") {
		t.Errorf("tunnelled body = %q", body)
	}
	if opens, _ := session.counts(); opens != 1 {
		t.Errorf("session opened %d times, want 1", opens)
	}
}

func TestRejectsARequestWithoutADestination(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	addr := serve(t, newTestProxy(t, session, nil))

	resp, err := proxyClient().Get(addr + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestClosesTheOwnedSessionWhenTheProxyStops(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	p := newTestProxy(t, session, nil)
	addr := serve(t, p)

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	resp.Body.Close()

	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})
}

func TestKeepsTheSessionUntilTheBillingBoundary(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	const unit = 300 * time.Millisecond
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.TimeUnit = unit
	}))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("request through the proxy: %v", err)
	}
	resp.Body.Close()

	time.Sleep(unit / 4)
	if _, closes := session.counts(); closes != 0 {
		t.Fatal("the session was closed before the billing boundary")
	}
	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})
}

func TestClosesImmediatelyWhenTheUnitHasAlreadyElapsed(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	const unit = 50 * time.Millisecond
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.TimeUnit = unit
	}))

	for i := 0; i < 4; i++ {
		resp, err := throughProxy(t, addr, origin.URL)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		resp.Body.Close()
		time.Sleep(unit)
	}

	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 4
	})
}

func TestAConnectionDuringTheBillingHoldKeepsTheSession(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reply(w, "ok")
	}))
	defer origin.Close()

	session := &countingSession{}
	const unit = 400 * time.Millisecond
	addr := serve(t, newTestProxy(t, session, func(o *Options) {
		o.TimeUnit = unit
	}))

	resp, err := throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	resp.Body.Close()

	// Una segunda conexión llega durante la espera de facturación: debe reutilizar
	// la sesión, y el cierre pendiente debe apartarse.
	time.Sleep(unit / 4)
	resp, err = throughProxy(t, addr, origin.URL)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "ok") {
		t.Fatalf("second request body = %q", body)
	}

	if opens, _ := session.counts(); opens != 1 {
		t.Errorf("session opened %d times, want 1: the second connection reused it", opens)
	}

	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})
}

func TestAReleaseAfterADeclinedCloseSchedulesANewOne(t *testing.T) {
	session := &countingSession{}
	g := newGate(session, 0, time.Second, log.New(io.Discard, "", 0))

	release, err := g.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}

	g.mu.Lock()
	g.pending++
	generation, openedAt := g.generation, g.openedAt
	g.mu.Unlock()
	if _, _, closing := g.beginClosing(generation, openedAt); closing {
		t.Fatal("beginClosing closed a session that is still in use")
	}

	release()
	waitFor(t, func() bool {
		_, closes := session.counts()
		return closes == 1
	})
}

func TestNoTunnelIsTrackedOnceTheProxyIsClosing(t *testing.T) {
	p := newTestProxy(t, &countingSession{}, nil)
	p.closeTunnels()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if p.trackTunnel(server) {
		t.Error("a tunnel was tracked after the proxy started closing")
	}
}

func TestDrainWaitsForALoginInProgress(t *testing.T) {
	g := newGate(&countingSession{}, 0, time.Second, log.New(io.Discard, "", 0))
	g.mu.Lock()
	g.opening = true
	g.mu.Unlock()

	done := make(chan struct{})
	go func() {
		g.drain()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("drain returned while a login was still in progress")
	case <-time.After(100 * time.Millisecond):
	}

	g.mu.Lock()
	g.opening = false
	g.mu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drain did not return after the login finished")
	}
}
