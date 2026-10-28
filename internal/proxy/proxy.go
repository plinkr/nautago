// Package proxy abre una sesión Nauta con la primera petición, la comparte entre
// las concurrentes y la cierra al irse la última. Nunca cierra una sesión que no
// abrió: pertenece a otro proceso, como un `nauta up` en otra terminal.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Session interface {
	Exists() bool
	Open(ctx context.Context) error
	Close(ctx context.Context) error
}

type Options struct {
	// Host es loopback por defecto: el proxy no autentica, y abierto a la red
	// cualquiera gastaría la sesión pagada del usuario.
	Host string
	Port int
	// TimeUnit es el periodo mínimo de facturación. La sesión se mantiene hasta
	// el siguiente múltiplo, porque esa fracción ya está cobrada.
	TimeUnit     time.Duration
	MaxConn      int
	Log          io.Writer
	Allow        []string
	Deny         []string
	Session      Session
	CloseTimeout time.Duration
}

const loopbackHost = "127.0.0.1"

type Proxy struct {
	opts      Options
	log       *log.Logger
	transport http.RoundTripper
	gate      *gate
	slots     chan struct{}

	// http.Server olvida las conexiones secuestradas por CONNECT; sin este
	// registro el cierre no podría terminarlas.
	tunnelsMu sync.Mutex
	tunnels   map[net.Conn]struct{}
	// Un túnel que se registre después del barrido de cierre sostendría la
	// sesión indefinidamente.
	tunnelsClosed bool
}

func New(opts Options) (*Proxy, error) {
	if opts.Session == nil {
		return nil, errors.New("el proxy necesita una sesión Nauta")
	}
	if opts.TimeUnit <= 0 {
		return nil, errors.New("la unidad de tiempo es obligatoria (por ejemplo 120s)")
	}
	if opts.CloseTimeout <= 0 {
		opts.CloseTimeout = 30 * time.Second
	}
	if opts.Host == "" {
		opts.Host = loopbackHost
	}

	logger := log.New(io.Discard, "", log.LstdFlags)
	if opts.Log != nil {
		logger = log.New(opts.Log, "", log.LstdFlags)
	}

	// Clonar conserva los límites de conexiones inactivas y de handshake; un
	// transporte vacío dejaría una conexión ociosa por cada destino visitado.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second}).DialContext

	p := &Proxy{
		opts:      opts,
		log:       logger,
		transport: transport,
		gate:      newGate(opts.Session, opts.TimeUnit, opts.CloseTimeout, logger),
		tunnels:   make(map[net.Conn]struct{}),
	}
	// Sin límite el canal queda nil; uno sin búfer rechazaría todo.
	if opts.MaxConn > 0 {
		p.slots = make(chan struct{}, opts.MaxConn)
	}
	return p, nil
}

func (p *Proxy) Serve(ctx context.Context) error {
	if p.opts.Port <= 0 || p.opts.Port > 65535 {
		return fmt.Errorf("puerto inválido: %d", p.opts.Port)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(p.opts.Host, strconv.Itoa(p.opts.Port)))
	if err != nil {
		return fmt.Errorf("escuchar en %s:%d: %w", p.opts.Host, p.opts.Port, err)
	}
	return p.ServeListener(ctx, listener)
}

func (p *Proxy) ServeListener(ctx context.Context, listener net.Listener) error {
	p.log.Printf("proxy escuchando en %s (unidad de tiempo: %s)", listener.Addr(), p.opts.TimeUnit)

	server := &http.Server{
		Handler: http.HandlerFunc(p.serve),
		// Evita que un cliente retenga la conexión enviando cabeceras a goteo.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Shutdown no toca conexiones secuestradas, y los túneles abiertos mantienen
	// la sesión en uso; hay que cerrarlos a mano.
	go func() {
		<-ctx.Done()
		p.closeTunnels()
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.opts.CloseTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			p.log.Printf("el cierre del proxy agotó su margen: %v", err)
		}
	}()

	err := server.Serve(listener)
	// Si la unidad de facturación pendiente supera CloseTimeout, la sesión queda
	// abierta y la cierra `nauta down`.
	p.gate.drain()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (p *Proxy) closeTunnels() {
	p.tunnelsMu.Lock()
	defer p.tunnelsMu.Unlock()
	p.tunnelsClosed = true
	for conn := range p.tunnels {
		conn.Close()
	}
}

func (p *Proxy) trackTunnel(conn net.Conn) bool {
	p.tunnelsMu.Lock()
	defer p.tunnelsMu.Unlock()
	if p.tunnelsClosed {
		return false
	}
	p.tunnels[conn] = struct{}{}
	return true
}

func (p *Proxy) untrackTunnel(conn net.Conn) {
	p.tunnelsMu.Lock()
	defer p.tunnelsMu.Unlock()
	delete(p.tunnels, conn)
}

func (p *Proxy) serve(w http.ResponseWriter, r *http.Request) {
	if p.slots != nil {
		select {
		case p.slots <- struct{}{}:
			defer func() { <-p.slots }()
		default:
			http.Error(w, "Se alcanzó el máximo de conexiones simultáneas", http.StatusServiceUnavailable)
			return
		}
	}

	destination, host := requestDestination(r)
	if destination == "" {
		http.Error(w, "Solicitud sin destino", http.StatusBadRequest)
		return
	}
	if !p.allowed(host) {
		p.log.Printf("bloqueado: %s", host)
		http.Error(w, "Dominio bloqueado por el proxy", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		p.serveConnect(w, r, destination)
		return
	}
	p.serveForward(w, r)
}

func (p *Proxy) serveForward(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "Se esperaba una URL absoluta", http.StatusBadRequest)
		return
	}

	release, err := p.gate.acquire(r.Context())
	if err != nil {
		http.Error(w, sessionUnavailable, http.StatusBadGateway)
		return
	}
	defer release()

	outbound := r.Clone(r.Context())
	outbound.RequestURI = ""

	resp, err := p.transport.RoundTrip(outbound)
	if err != nil {
		p.log.Printf("fallo %s %s: %v", r.Method, r.URL, err)
		http.Error(w, "No se pudo conectar con el destino", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// Las cabeceras ya salieron: un corte a mitad del cuerpo no tiene arreglo.
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) serveConnect(w http.ResponseWriter, r *http.Request, destination string) {
	release, err := p.gate.acquire(r.Context())
	if err != nil {
		http.Error(w, sessionUnavailable, http.StatusBadGateway)
		return
	}
	defer release()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "El túnel no está disponible", http.StatusInternalServerError)
		return
	}

	client, _, err := hijacker.Hijack()
	if err != nil {
		p.log.Printf("no se pudo tomar la conexión: %v", err)
		return
	}
	defer client.Close()
	if !p.trackTunnel(client) {
		return
	}
	defer p.untrackTunnel(client)

	upstream, err := net.DialTimeout("tcp", destination, 30*time.Second)
	if err != nil {
		p.log.Printf("no se pudo conectar con %s: %v", destination, err)
		// Mejor esfuerzo: la conexión se cierra enseguida.
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer upstream.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// El error de la copia solo indica qué lado cortó primero.
		_, _ = io.Copy(upstream, client)
		// Half-close para que el destino vea el EOF del cliente.
		if conn, ok := upstream.(*net.TCPConn); ok {
			_ = conn.CloseWrite()
		}
	}()
	_, _ = io.Copy(client, upstream)
	if conn, ok := client.(*net.TCPConn); ok {
		_ = conn.CloseWrite()
	}
	<-done
}

func (p *Proxy) allowed(host string) bool {
	host = normalizeHost(host)
	for _, pattern := range p.opts.Deny {
		if matchesHost(host, pattern) {
			return false
		}
	}
	if len(p.opts.Allow) == 0 {
		return true
	}
	for _, pattern := range p.opts.Allow {
		if matchesHost(host, pattern) {
			return true
		}
	}
	return false
}

func matchesHost(host, pattern string) bool {
	pattern = strings.TrimPrefix(normalizeHost(strings.TrimSpace(pattern)), "*.")
	return pattern != "" && (pattern == host || strings.HasSuffix(host, "."+pattern))
}

// "example.com." resuelve al mismo sitio; sin quitar el punto final bastaría
// para saltarse --deny.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(host), ".")
}

// destination lleva puerto para conectar; host es solo el nombre, para filtrar.
func requestDestination(r *http.Request) (destination, host string) {
	if r.Method == http.MethodConnect {
		address := r.Host
		if parsed, err := url.Parse("http://" + address); err == nil {
			return address, parsed.Hostname()
		}
		return address, address
	}
	if r.URL == nil || !r.URL.IsAbs() {
		return "", ""
	}
	return r.URL.Host, r.URL.Hostname()
}

// Vago a propósito: los clientes del proxy no deben ver el estado del portal.
// La causa real va al log.
const sessionUnavailable = "No se pudo establecer la sesión Nauta"

// gate cuenta referencias a la sesión compartida para cerrarla exactamente
// cuando termina la última conexión.
type gate struct {
	session      Session
	timeUnit     time.Duration
	closeTimeout time.Duration
	log          *log.Logger

	mu    sync.Mutex
	idle  *sync.Cond
	refs  int
	owned bool
	// El login corre sin el candado; opening evita que otra conexión arranque
	// un segundo login mientras tanto.
	opening bool
	// Mientras closing, las peticiones esperan: usarían una sesión a punto de
	// desaparecer.
	closing bool
	// generation evita que un cierre programado para una sesión anterior cierre
	// la que la sustituyó.
	generation int
	openedAt   time.Time
	// pending evita programar un segundo cierre cuando ya hay uno esperando el
	// límite de facturación.
	pending int
}

func newGate(session Session, timeUnit time.Duration, closeTimeout time.Duration, logger *log.Logger) *gate {
	g := &gate{
		session:      session,
		timeUnit:     timeUnit,
		closeTimeout: closeTimeout,
		log:          logger,
	}
	g.idle = sync.NewCond(&g.mu)
	return g
}

func (g *gate) acquire(ctx context.Context) (func(), error) {
	if err := g.waitForTurn(ctx); err != nil {
		return nil, err
	}

	// Una sesión ajena, o una propia que espera el límite de facturación, se
	// reutiliza tal cual.
	if g.refs > 0 || g.owned || g.session.Exists() {
		g.refs++
		g.mu.Unlock()
		return releaser(g.release), nil
	}

	// El candado se suelta durante el login para no bloquear cada release
	// mientras el portal contesta.
	g.opening = true
	g.mu.Unlock()

	// La sesión es del proxy, no de la petición que la disparó.
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.closeTimeout)
	err := g.session.Open(openCtx)
	cancel()

	g.mu.Lock()
	g.opening = false
	if err != nil {
		g.idle.Broadcast()
		g.mu.Unlock()
		g.log.Printf("no se pudo abrir la sesión Nauta: %v", err)
		return nil, err
	}
	// Propiedad y primera referencia juntas: un cierre nunca ve una sesión
	// propia sin referencias.
	g.owned = true
	g.openedAt = time.Now()
	g.generation++
	g.refs++
	g.idle.Broadcast()
	g.mu.Unlock()

	g.log.Printf("sesión Nauta abierta (unidad de tiempo: %s)", g.timeUnit)
	return releaser(g.release), nil
}

// waitForTurn respeta ctx para que un cliente que abandona no retenga su
// goroutine ni su plaza de MaxConn durante un cierre.
func (g *gate) waitForTurn(ctx context.Context) error {
	g.mu.Lock()
	for g.closing || g.opening {
		// sync.Cond no conoce contextos; la cancelación despierta a los que esperan.
		stop := context.AfterFunc(ctx, func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.idle.Broadcast()
		})
		g.idle.Wait()
		stop()
		if err := ctx.Err(); err != nil {
			g.mu.Unlock()
			return err
		}
	}
	return nil
}

func releaser(release func()) func() {
	var once sync.Once
	return func() { once.Do(release) }
}

// El cierre corre en otra goroutine: esperar el límite de facturación no puede
// retrasar la respuesta al cliente.
func (g *gate) release() {
	g.mu.Lock()
	if g.refs > 0 {
		g.refs--
	}
	if g.refs > 0 || !g.owned {
		g.mu.Unlock()
		return
	}
	if g.pending > 0 {
		g.mu.Unlock()
		return
	}
	generation, openedAt := g.generation, g.openedAt
	g.pending++
	g.mu.Unlock()

	go g.shutdown(generation, openedAt)
}

// pending baja bajo el mismo candado en que se decide no cerrar o se termina de
// cerrar. Si bajara después, un release intermedio vería un cierre "pendiente"
// que ya se apartó, no programaría otro, y la sesión quedaría abierta.
func (g *gate) shutdown(generation int, openedAt time.Time) {
	for {
		g.holdForTimeUnit(openedAt)

		var closing bool
		generation, openedAt, closing = g.beginClosing(generation, openedAt)
		if closing {
			break
		}
		if generation == 0 {
			// La sesión volvió a usarse; esa conexión programará el próximo cierre.
			return
		}
	}
	defer g.endClosing()

	ctx, cancel := context.WithTimeout(context.Background(), g.closeTimeout)
	defer cancel()
	if err := g.session.Close(ctx); err != nil {
		g.log.Printf("no se pudo cerrar la sesión Nauta: %v", err)
		// Conservar la propiedad para que la siguiente conexión reintente el
		// cierre en vez de tratarla como ajena.
		g.mu.Lock()
		g.owned = true
		g.mu.Unlock()
		return
	}
	g.log.Printf("sesión Nauta cerrada")
}

// beginClosing devuelve una de tres decisiones:
//   - cerrar (closing true);
//   - seguir a la sesión que sustituyó a la esperada (su generación), porque
//     mientras este cierre cuente como pendiente nadie más la cerrará;
//   - apartarse (generación 0) porque está en uso; deja de contar como
//     pendiente para que quien la usa programe el siguiente cierre.
func (g *gate) beginClosing(generation int, openedAt time.Time) (int, time.Time, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refs > 0 || !g.owned {
		g.pending--
		return 0, time.Time{}, false
	}
	if g.generation != generation {
		return g.generation, g.openedAt, false
	}
	g.closing = true
	g.owned = false
	return generation, openedAt, true
}

func (g *gate) endClosing() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closing = false
	g.pending--
	g.idle.Broadcast()
}

// holdForTimeUnit espera al siguiente límite de facturación: esa fracción ya está
// cobrada, y otra petición dentro de ella reutiliza la sesión.
func (g *gate) holdForTimeUnit(openedAt time.Time) {
	if g.timeUnit <= 0 {
		return
	}
	elapsed := time.Since(openedAt)
	remainder := elapsed % g.timeUnit
	if remainder == 0 {
		return
	}

	wait := g.timeUnit - remainder
	g.log.Printf("esperando %s para cerrar en el límite de facturación", wait.Round(time.Second))
	time.Sleep(wait)
}

// drain espera, como mucho closeTimeout, a conexiones y cierres en curso: salir
// antes abandonaría una sesión que el proxy abrió y que sigue facturando.
func (g *gate) drain() {
	deadline := time.Now().Add(g.closeTimeout)
	for {
		g.mu.Lock()
		// Un login en curso aún no tiene referencias, pero será del proxy.
		busy := g.pending > 0 || g.refs > 0 || g.opening
		g.mu.Unlock()
		if !busy || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
