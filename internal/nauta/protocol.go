package nauta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/html"
)

const (
	portalRoot = "https://secure.etecsa.net:8443"

	queryServlet = portalRoot + "/EtecsaQueryServlet"

	logoutServlet = portalRoot + "/LogoutServlet"

	// Con la red interceptada, este sitio responde con una página del portal.
	checkPageLocal = "http://www.cubadebate.cu/"

	// Desde la salida de un VPN un destino cubano puede no responder. Sin una
	// alternativa, esa red se tomaría por muerta y se cerraría el VPN de alguien
	// que sí tenía internet.
	checkPageAnywhere = "http://example.com/"

	onlinePath = "online.do"

	logoutSuccess = "SUCCESS"

	formID = "formulario"

	sessionInfoID = "sessioninfo"
)

var checkPages = []string{checkPageLocal, checkPageAnywhere}

// portalMarker se deriva de portalRoot para que cambiar el host no deje la
// detección de intercepción comparando contra un valor obsoleto.
var portalMarker = mustHost(portalRoot)

const (
	// El portal es local: una respuesta lenta ya indica un problema.
	DefaultTimeout = 30 * time.Second

	checkTimeout = 3 * time.Second

	// Las páginas del portal pesan unos pocos KB; más que esto es algo inesperado.
	maxBody = 4 << 20
)

var (
	attributeUUIDPattern = regexp.MustCompile(`ATTRIBUTE_UUID=(\w+)&CSRFHW=`)
	alertReasonPattern   = regexp.MustCompile(`alert\("([^"]*?)"\)`)
)

type Protocol struct {
	client *http.Client
	// Sin cookies a propósito: el sondeo debe ver lo que vería cualquier cliente
	// de la red, no lo que guarde esta sesión.
	checker *http.Client
}

func NewProtocol(client *http.Client) *Protocol {
	if client == nil {
		client = DefaultHTTPClient()
	}

	probe := *client
	probe.Jar = nil
	if probe.Timeout == 0 || probe.Timeout > checkTimeout {
		probe.Timeout = checkTimeout
	}

	return &Protocol{client: client, checker: &probe}
}

func DefaultHTTPClient() *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		panic("nauta: cannot create cookie jar: " + err.Error())
	}
	return &http.Client{Timeout: DefaultTimeout, Jar: jar}
}

// IsConnected responde si hace falta un login de Nauta, así que un sondeo que no
// se completa cuenta como "no conectado".
func (p *Protocol) IsConnected(ctx context.Context) bool {
	for _, page := range checkPages {
		body, err := p.check(ctx, page)
		if err != nil {
			// Un destino inalcanzable no dice nada de la red; decide el siguiente.
			continue
		}
		// El portal intercepta todos los destinos por igual: probar otro no
		// cambiaría el veredicto.
		return !strings.Contains(string(body), portalMarker)
	}
	return false
}

func (p *Protocol) check(ctx context.Context, page string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.checker.Do(req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	return readBody(resp)
}

func (p *Protocol) StartSession(ctx context.Context) (*Session, error) {
	resp, err := p.do(ctx, http.MethodGet, portalRoot, nil)
	if err != nil {
		return nil, err
	}
	body := mustRead(resp)
	if !okStatus(resp) {
		return nil, portalErrorf("No se pudo crear la sesión: %s", statusLine(resp))
	}

	// La página de entrada es un formulario que el navegador reenvía solo; sus
	// inputs son los parámetros de la siguiente petición.
	landing := formInputs(parseDocument(string(body)))

	resp, err = p.do(ctx, http.MethodPost, portalRoot, encodeForm(landing))
	if err != nil {
		return nil, err
	}
	doc := parseDocument(string(mustRead(resp)))

	form := formByID(doc, formID)
	if form == nil {
		return nil, portalErrorf("No se pudo crear la sesión: el portal no devolvió el formulario de acceso")
	}
	inputs := formInputs(form)

	action := attr(form, "action")
	if action == "" {
		action = portalRoot
	}

	return &Session{
		LoginAction: resolveAction(resp.Request.URL, action),
		CSRFHW:      inputs["CSRFHW"],
		WlanUserIP:  inputs["wlanuserip"],
	}, nil
}

func entryForm(session *Session) url.Values {
	return url.Values{
		"CSRFHW":     {session.CSRFHW},
		"wlanuserip": {session.WlanUserIP},
	}
}

func sessionForm(session *Session) url.Values {
	form := entryForm(session)
	form.Set("ATTRIBUTE_UUID", session.AttributeUUID)
	return form
}

func credentialsForm(session *Session, user, password string) url.Values {
	form := entryForm(session)
	form.Set("username", user)
	form.Set("password", password)
	return form
}

func (p *Protocol) Authenticate(ctx context.Context, session *Session, user, password string) error {
	resp, err := p.do(ctx, http.MethodPost, session.LoginAction, credentialsForm(session, user, password))
	if err != nil {
		return err
	}
	body := mustRead(resp)

	if !okStatus(resp) {
		return portalErrorf("Falló el inicio de sesión: %s", statusLine(resp))
	}

	if !strings.Contains(resp.Request.URL.String(), onlinePath) {
		return portalErrorf("Falló el inicio de sesión: %s", loginFailureReason(body))
	}

	// Llegar a online.do significa que la cuenta ya factura, como en NautaPy.
	// Un token ausente no invalida el login: tratarlo como fallo impediría
	// guardar la sesión y nadie podría intentar cerrarla.
	if token := attributeUUIDPattern.FindSubmatch(body); token != nil {
		session.AttributeUUID = string(token[1])
	}
	return nil
}

// Logout envía todos los tokens en el query string, por eso puede cerrar una
// sesión abierta por otro proceso.
func (p *Protocol) Logout(ctx context.Context, session *Session, user string) error {
	query := sessionForm(session)
	query.Set("username", user)

	resp, err := p.do(ctx, http.MethodPost, logoutServlet+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	body := mustRead(resp)

	if !okStatus(resp) {
		return portalErrorf("Fallo al cerrar la sesión: %s", statusLine(resp))
	}
	if !strings.Contains(strings.ToUpper(string(body)), logoutSuccess) {
		return portalErrorf("Fallo al cerrar la sesión: %s", preview(string(body), 100))
	}
	return nil
}

func (p *Protocol) RemainingTime(ctx context.Context, session *Session, user string) (string, error) {
	form := sessionForm(session)
	form.Set("op", "getLeftTime")
	form.Set("username", user)

	resp, err := p.do(ctx, http.MethodPost, queryServlet, form)
	if err != nil {
		return "", err
	}
	body := mustRead(resp)

	if !okStatus(resp) {
		return "", portalErrorf("Fallo al obtener el tiempo restante: %s", statusLine(resp))
	}
	return string(body), nil
}

func (p *Protocol) UserCredit(ctx context.Context, session *Session, user, password string) (string, error) {
	resp, err := p.do(ctx, http.MethodPost, queryServlet, credentialsForm(session, user, password))
	if err != nil {
		return "", err
	}
	body := mustRead(resp)

	if !okStatus(resp) {
		return "", portalErrorf("Fallo al obtener la información del usuario: %s", statusLine(resp))
	}
	if !strings.Contains(resp.Request.URL.String(), portalMarker) {
		return "", portalErrorf("No se puede obtener el crédito del usuario mientras está online")
	}

	credit := sessionInfoCell(parseDocument(string(body)))
	if credit == "" {
		return "", portalErrorf("Fallo al obtener el crédito del usuario: no se encontró la información")
	}
	return credit, nil
}

func sessionInfoCell(doc *html.Node) string {
	table := findByID(doc, sessionInfoID)
	if table == nil {
		return ""
	}
	section := findTag(table, "tbody")
	if section == nil {
		return ""
	}
	cell := nthChild(nthChild(section, "tr", 2), "td", 2)
	if cell == nil {
		return ""
	}
	return cellText(cell)
}

func loginFailureReason(body []byte) string {
	if match := alertReasonPattern.FindSubmatch(body); match != nil {
		return string(match[1])
	}
	return "el portal rechazó las credenciales"
}

// El llamador debe cerrar la respuesta con mustRead o drain.
func (p *Protocol) do(ctx context.Context, method, action string, form url.Values) (*http.Response, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, action, body)
	if err != nil {
		return nil, fmt.Errorf("build %s request for %s: %w", method, action, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// El query string de Logout lleva los tokens de sesión; no pueden acabar
		// en un mensaje de error.
		safe := *req.URL
		safe.RawQuery = ""
		safe.User = nil
		return nil, networkErrorf(method+" "+safe.String(), transportCause(err))
	}
	return resp, nil
}

// transportCause desenvuelve el *url.Error de http.Client porque imprime la URL
// completa, tokens incluidos.
func transportCause(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func encodeForm(inputs map[string]string) url.Values {
	form := url.Values{}
	for name, value := range inputs {
		form.Set(name, value)
	}
	return form
}

func okStatus(resp *http.Response) bool {
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func statusLine(resp *http.Response) string {
	reason := strings.TrimSpace(strings.TrimPrefix(resp.Status, strconv.Itoa(resp.StatusCode)))
	if reason == "" {
		reason = http.StatusText(resp.StatusCode)
	}
	return fmt.Sprintf("%d - %s", resp.StatusCode, reason)
}

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// Un cuerpo truncado es tan inservible como ninguno, así que el error de lectura
// se convierte en cuerpo vacío y lo detecta el análisis posterior.
func mustRead(resp *http.Response) []byte {
	body, err := readBody(resp)
	drain(resp)
	if err != nil {
		return nil
	}
	return body
}

// Vaciar el cuerpo permite reutilizar la conexión.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	resp.Body.Close()
}

func resolveAction(base *url.URL, action string) string {
	ref, err := url.Parse(action)
	if err != nil || ref.IsAbs() {
		return action
	}
	if base == nil {
		return action
	}
	return base.ResolveReference(ref).String()
}

func mustHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic("nauta: invalid portal URL: " + err.Error())
	}
	return parsed.Hostname()
}

func preview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
