package nauta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// El portal suele estar inalcanzable justo al desconectar, pero un reintento sin
// límite es peor que un fallo honesto.
const maxDisconnectAttempts = 5

const defaultRetryDelay = 10 * time.Second

type ConnectionLog interface {
	SaveLogin(user string, at time.Time) error
	SaveLogout(user string, at time.Time) error
}

type Config struct {
	SessionPath    string
	HTTPClient     *http.Client
	Log            ConnectionLog
	OnHistoryError func(error)
	RetryDelay     time.Duration
	Now            func() time.Time
}

type Client struct {
	user     string
	password string
	cfg      Config
	protocol *Protocol
	session  *Session
}

func NewClient(user, password string, cfg Config) *Client {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RetryDelay == 0 {
		cfg.RetryDelay = defaultRetryDelay
	}
	return &Client{
		user:     user,
		password: password,
		cfg:      cfg,
		protocol: NewProtocol(cfg.HTTPClient),
	}
}

func (c *Client) User() string { return c.user }

func (c *Client) IsLoggedIn() bool { return SessionExists(c.cfg.SessionPath) }

func (c *Client) IsConnected(ctx context.Context) bool { return c.protocol.IsConnected(ctx) }

func (c *Client) Login(ctx context.Context) error {
	// El sondeo va antes que el archivo de sesión, como en NautaPy: si el portal
	// intercepta, un archivo guardado es una sesión que ya no existe.
	if c.protocol.IsConnected(ctx) {
		if c.IsLoggedIn() {
			return ErrAlreadyLoggedIn
		}
		return ErrAlreadyConnected
	}
	// La sesión guardada no se borra aquí; SaveSession la reemplaza si el login
	// sale bien. Con un sondeo falso de "sin conexión", borrarla dejaría sin
	// tokens a otra terminal que sí tiene la sesión abierta.
	session, err := c.protocol.StartSession(ctx)
	if err != nil {
		return err
	}
	if err := c.protocol.Authenticate(ctx, session, c.user, c.password); err != nil {
		return err
	}
	session.Username = c.user
	c.session = session

	if err := SaveSession(c.cfg.SessionPath, session); err != nil {
		// Sin el archivo ningún otro comando podría cerrar esta sesión, que ya
		// factura.
		c.abandon(ctx)
		return err
	}
	return nil
}

// abandon usa su propio plazo porque el contexto del llamador puede estar
// cancelado. El error se descarta: el usuario ya recibe el fallo del login.
func (c *Client) abandon(ctx context.Context) {
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), defaultRetryDelay)
	defer cancel()
	_ = c.Logout(shutdown)
}

// LoadSession también adopta el usuario guardado, que es quien debe aparecer en
// el historial al cerrar.
func (c *Client) LoadSession() error {
	session, err := LoadSession(c.cfg.SessionPath)
	if err != nil {
		return err
	}
	c.session = session
	if session.Username != "" {
		c.user = session.Username
	}
	return nil
}

// LoadSessionTokens no cambia el usuario: una consulta por otra cuenta tiene que
// preguntar por esa cuenta, no por la que abrió la sesión.
func (c *Client) LoadSessionTokens() error {
	session, err := LoadSession(c.cfg.SessionPath)
	if err != nil {
		return err
	}
	c.session = session
	return nil
}

// Sin sesión guardada basta una vacía: las consultas de cuenta solo necesitan
// el usuario.
func (c *Client) ensureSession() error {
	if c.session != nil {
		return nil
	}
	if err := c.LoadSessionTokens(); err != nil {
		if !errors.Is(err, ErrNoSession) {
			return err
		}
		c.session = &Session{}
	}
	return nil
}

func (c *Client) RemainingTime(ctx context.Context) (string, error) {
	if err := c.ensureSession(); err != nil {
		return "", err
	}
	return c.protocol.RemainingTime(ctx, c.session, c.user)
}

func (c *Client) UserCredit(ctx context.Context) (string, error) {
	if err := c.ensureSession(); err != nil {
		return "", err
	}
	return c.protocol.UserCredit(ctx, c.session, c.user, c.password)
}

// Logout solo borra la sesión guardada cuando el portal confirma la
// desconexión: esos tokens son la única forma de reintentar, y sin ellos la
// cuenta seguiría facturando. El historial se cierra igualmente porque el
// intento ocurrió; su fallo va a OnHistoryError, ya que no invalida el cierre.
func (c *Client) Logout(ctx context.Context) error {
	if c.session == nil {
		if err := c.LoadSession(); err != nil {
			return err
		}
	}

	disconnectErr := c.disconnect(ctx)

	var disposeErr error
	if disconnectErr == nil {
		disposeErr = DisposeSession(c.cfg.SessionPath)
	}
	if disconnectErr == nil && disposeErr == nil {
		c.session = nil
	}
	if err := c.recordLogout(); err != nil && c.cfg.OnHistoryError != nil {
		c.cfg.OnHistoryError(err)
	}

	return errors.Join(disconnectErr, disposeErr)
}

func (c *Client) disconnect(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= maxDisconnectAttempts; attempt++ {
		lastErr = c.protocol.Logout(ctx, c.session, c.user)
		if lastErr == nil {
			return nil
		}

		// Un rechazo del portal se repetiría igual; solo se reintenta la red.
		var netErr *NetworkError
		if !errors.As(lastErr, &netErr) {
			return lastErr
		}
		if attempt == maxDisconnectAttempts {
			break
		}
		if err := sleep(ctx, c.cfg.RetryDelay); err != nil {
			return lastErr
		}
	}

	return portalErrorf("Hay problemas en la red y no se puede cerrar la sesión.\n"+
		"Es posible que ya esté desconectado. Intente con 'nauta down' dentro de unos minutos: %w", lastErr)
}

func (c *Client) recordLogout() error {
	if c.cfg.Log == nil || c.user == "" {
		return nil
	}
	return c.cfg.Log.SaveLogout(c.user, c.cfg.Now())
}

func (c *Client) RecordLogin() error {
	if c.cfg.Log == nil {
		return nil
	}
	if err := c.cfg.Log.SaveLogin(c.user, c.cfg.Now()); err != nil {
		return fmt.Errorf("registrar la conexión: %w", err)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
