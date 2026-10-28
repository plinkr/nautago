package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/plinkr/nautago/internal/nauta"
)

func TestUsersAddListAndRemove(t *testing.T) {
	app := newTestApp(t)

	if code := app.run("users", "add", "plinkr@nauta.com.cu", "s3cret"); code != 0 {
		t.Fatalf("users add exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Usuario guardado: plinkr@nauta.com.cu\n" {
		t.Errorf("users add printed %q", got)
	}

	app.out.Reset()
	if code := app.run("users", "list"); code != 0 {
		t.Fatalf("users list exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "plinkr@nauta.com.cu\n" {
		t.Errorf("users list printed %q", got)
	}

	app.out.Reset()
	if code := app.run("users", "remove", "plinkr@nauta.com.cu"); code != 0 {
		t.Fatalf("users remove exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Usuario eliminado: plinkr@nauta.com.cu\n" {
		t.Errorf("users remove printed %q", got)
	}

	app.out.Reset()
	app.run("users", "list")
	if got := app.out.String(); got != "" {
		t.Errorf("users list after remove printed %q", got)
	}
}

func TestUsersAddAsksForThePassword(t *testing.T) {
	app := newTestApp(t)
	app.In = strings.NewReader("typed\n")

	if code := app.run("users", "add", "plinkr@nauta.com.cu"); code != 0 {
		t.Fatalf("users add exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.err.String(), "Contraseña para plinkr@nauta.com.cu:") {
		t.Errorf("the prompt was not shown, stderr: %q", app.err.String())
	}

	_, password, err := app.Store.FindCredentials("plinkr", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if password != "typed" {
		t.Errorf("stored password = %q, want %q", password, "typed")
	}
}

func TestUsersSetDefaultAndPassword(t *testing.T) {
	app := newTestApp(t)
	app.run("users", "add", "a@nauta.com.cu", "one")
	app.run("users", "add", "b@nauta.com.cu", "two")
	app.out.Reset()

	if code := app.run("users", "set-default", "b@nauta.com.cu"); code != 0 {
		t.Fatalf("users set-default exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Usuario predeterminado: b@nauta.com.cu\n" {
		t.Errorf("users set-default printed %q", got)
	}

	def, err := app.Store.DefaultUser()
	if err != nil {
		t.Fatalf("DefaultUser: %v", err)
	}
	if def != "b@nauta.com.cu" {
		t.Errorf("default user = %q", def)
	}

	app.out.Reset()
	if code := app.run("users", "set-password", "b@nauta.com.cu", "changed"); code != 0 {
		t.Fatalf("users set-password exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Contraseña actualizada: b@nauta.com.cu\n" {
		t.Errorf("users set-password printed %q", got)
	}
	_, password, err := app.Store.FindCredentials("b", "")
	if err != nil {
		t.Fatalf("FindCredentials: %v", err)
	}
	if password != "changed" {
		t.Errorf("stored password = %q", password)
	}
}

func TestUsersWithoutArgumentsShowsUsage(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("users"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.out.String(), "set-default") {
		t.Errorf("usage was not printed: %q", app.out.String())
	}
}

func TestUsersRejectsAnUnknownSubcommand(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("users", "explode"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.err.String(), "subcomando desconocido") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestIsLoggedIn(t *testing.T) {
	app := newTestApp(t)

	if code := app.run("is-logged-in"); code != 0 {
		t.Fatalf("is-logged-in exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Sesión activa: No\n" {
		t.Errorf("is-logged-in printed %q", got)
	}

	app.writeSession(t)
	app.out.Reset()
	app.run("is-logged-in")
	if got := app.out.String(); got != "Sesión activa: Sí\n" {
		t.Errorf("is-logged-in printed %q", got)
	}
}

func TestIsOnline(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)

	if code := app.run("is-online"); code != 0 {
		t.Fatalf("is-online exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "Online: No\n" {
		t.Errorf("is-online printed %q, want the intercepted answer", got)
	}

	portal.online = true
	app.out.Reset()
	app.run("is-online")
	if got := app.out.String(); got != "Online: Sí\n" {
		t.Errorf("is-online printed %q, want the connected answer", got)
	}
}

func TestDownWithoutASession(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("down"); code != 0 {
		t.Fatalf("down exited %d: %s", code, app.err)
	}
	if got := app.out.String(); got != "No hay ninguna sesión activa\n" {
		t.Errorf("down printed %q", got)
	}
}

func TestDownClosesTheStoredSession(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.writeSession(t)

	if code := app.run("down"); code != 0 {
		t.Fatalf("down exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.out.String(), "Sesión cerrada con éxito") {
		t.Errorf("down printed %q", app.out.String())
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("the session file survived the disconnect")
	}
	// La salida tiene que llegar al portal con los tokens guardados, o
	// `nauta down` cerraría una sesión distinta de la que recibió.
	if len(portal.logouts) != 1 {
		t.Fatalf("the portal saw %d logouts, want 1", len(portal.logouts))
	}
	if got := portal.logouts[0].Get("ATTRIBUTE_UUID"); got != "an-attribute-uuid" {
		t.Errorf("ATTRIBUTE_UUID = %q, want the stored token", got)
	}
	if got := portal.logouts[0].Get("username"); got != "plinkr@nauta.com.cu" {
		t.Errorf("username = %q, want the stored user", got)
	}
}

func TestDownKeepsTheSessionWhenTheNetworkFails(t *testing.T) {
	app := newTestApp(t)
	app.writeSession(t)

	app.HTTPClient = &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return nil, errors.New("network is unreachable")
			},
		},
	}
	app.RetryDelay = time.Millisecond

	if code := app.run("down"); code != 1 {
		t.Fatalf("down exited %d, want 1", code)
	}
	if _, err := statFile(app.sessionPath()); err != nil {
		t.Error("the session was given up although the portal never answered, so the retry is impossible")
	}

	message := app.err.String()
	if !strings.Contains(message, "problema en la red") {
		t.Errorf("stderr = %q, want the network guidance", message)
	}
	if !strings.Contains(message, "nauta down") {
		t.Errorf("stderr = %q, want the advice to retry the disconnect", message)
	}
}

func TestCredentialsAreRequired(t *testing.T) {
	app := newTestApp(t)
	code := app.run("up")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.err.String(), "No existe ningún usuario") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestUpRefusesWhenAlreadyConnected(t *testing.T) {
	portal := newFakePortal(t)
	portal.online = true
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("up"); code != 1 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.err.String(), "Hay una conexión activa") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestUpInBatchModeLeavesTheSessionOpen(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("up", "--batch"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	output := app.out.String()
	for _, want := range []string{
		"Conectando usuario: plinkr@nauta.com.cu",
		"[Sesión iniciada: ",
		"Tiempo restante: 02:14:24",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("up printed %q, want it to contain %q", output, want)
		}
	}
	if strings.Contains(output, "Cerrando sesión") {
		t.Error("batch mode closed the session it was asked to leave open")
	}
	if _, err := statFile(app.sessionPath()); err != nil {
		t.Error("batch mode did not leave a reusable session behind")
	}

	connections, err := app.Store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 0 {
		t.Errorf("batch mode recorded %d connections, want none", len(connections))
	}
}

func TestUpHonoursTheSessionTime(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	started := time.Now()
	if code := app.run("up", "-t", "1"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	elapsed := time.Since(started)

	output := app.out.String()
	if !strings.Contains(output, "La sesión se cerrará en") {
		t.Errorf("up printed %q, want the remaining time notice", output)
	}
	if !strings.Contains(output, "Cerrando sesión") {
		t.Errorf("up printed %q, want the shutdown notice", output)
	}
	if !strings.Contains(output, "Sesión cerrada con éxito") {
		t.Errorf("up printed %q, want the closing notice", output)
	}
	if elapsed > 20*time.Second {
		t.Errorf("up took %s, want it to stop soon after its limit", elapsed)
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("up left the session file behind")
	}
}

func TestUpNoLogSkipsTheHistory(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

	if code := app.run("up", "-t", "1", "--no-log"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	connections, err := app.Store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 0 {
		t.Errorf("up recorded %d connections, want none", len(connections))
	}
}

func TestUpRecordsTheConnection(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

	if code := app.run("up", "-t", "1"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	connections, err := app.Store.Connections()
	if err != nil {
		t.Fatalf("Connections: %v", err)
	}
	if len(connections) != 1 {
		t.Fatalf("recorded %d connections, want 1", len(connections))
	}
	if connections[0].End == nil {
		t.Error("the connection was left open")
	}
	if connections[0].User != "plinkr@nauta.com.cu" {
		t.Errorf("recorded user = %q", connections[0].User)
	}
}

func TestUpReportsARejectedLogin(t *testing.T) {
	portal := newFakePortal(t)
	portal.pageServed = "failed_login.html"
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "wrong")
	app.out.Reset()

	if code := app.run("up"); code != 1 {
		t.Fatalf("up exited %d: %s", code, app.out.String())
	}
	if !strings.Contains(app.err.String(), "El usuario o la contraseña son incorrectos") {
		t.Errorf("stderr = %q, want the reason the portal gave", app.err.String())
	}
}

func TestUpRefusesWhenASessionIsAlreadyOpen(t *testing.T) {
	portal := newFakePortal(t)
	portal.online = true
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.writeSession(t)
	app.out.Reset()

	if code := app.run("up"); code != 1 {
		t.Fatalf("up exited %d", code)
	}
	if !strings.Contains(app.err.String(), "Hay una sesión abierta") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestUpReplacesAStaleSessionWhileIntercepted(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.writeSession(t)
	app.out.Reset()

	if code := app.run("up", "-b"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err)
	}
	if !nauta.SessionExists(app.sessionPath()) {
		t.Error("the stale session was not replaced by a fresh login")
	}
}

func TestInfoUsesTheDefaultUser(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("info"); code != 0 {
		t.Fatalf("info exited %d: %s", code, app.err)
	}
	output := app.out.String()
	if !strings.Contains(output, "Usuario Nauta: plinkr@nauta.com.cu") {
		t.Errorf("info printed %q", output)
	}
	if !strings.Contains(output, "Tiempo restante: 02:14:24") {
		t.Errorf("info printed %q", output)
	}
}

func TestInfoAsksForTheRequestedUserWhileAnotherIsLoggedIn(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.run("users", "add", "pepe@nauta.com.cu", "s3cret")
	app.writeSession(t)
	app.out.Reset()

	if code := app.run("info", "pepe"); code != 0 {
		t.Fatalf("info exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.out.String(), "Usuario Nauta: pepe@nauta.com.cu") {
		t.Errorf("info printed %q", app.out.String())
	}
	if len(portal.queriedUsers) == 0 {
		t.Fatal("info never asked the portal")
	}
	for _, user := range portal.queriedUsers {
		if user != "pepe@nauta.com.cu" {
			t.Errorf("info asked the portal about %q, want pepe@nauta.com.cu", user)
		}
	}
}

func TestAdoptLegacySessionMovesTheNautaPySession(t *testing.T) {
	app := newTestApp(t)
	legacy := t.TempDir()
	legacyPath := filepath.Join(legacy, sessionFileName)
	nautaPySession := `{"login_action": "https://secure.etecsa.net:8443/LoginServlet", "csrfhw": "abc", ` +
		`"wlanuserip": "10.190.20.96", "attribute_uuid": "uuid", "username": "plinkr@nauta.com.cu"}`
	if err := os.WriteFile(legacyPath, []byte(nautaPySession), 0o644); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	adopted, err := app.AdoptLegacySession(legacy)
	if err != nil || !adopted {
		t.Fatalf("AdoptLegacySession = %v, %v; want the session adopted", adopted, err)
	}
	session, err := nauta.LoadSession(app.sessionPath())
	if err != nil {
		t.Fatalf("load adopted session: %v", err)
	}
	if session.AttributeUUID != "uuid" || session.Username != "plinkr@nauta.com.cu" {
		t.Errorf("adopted session = %+v", session)
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Error("the NautaPy session was left behind and would be adopted again after `nauta down`")
	}
	if info, err := os.Stat(app.sessionPath()); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("adopted session mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAdoptLegacySessionIgnoresAnUnreadableFile(t *testing.T) {
	app := newTestApp(t)
	legacy := t.TempDir()
	legacyPath := filepath.Join(legacy, sessionFileName)
	if err := os.WriteFile(legacyPath, []byte("# Netscape HTTP Cookie File\n"), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	if adopted, err := app.AdoptLegacySession(legacy); err != nil || adopted {
		t.Fatalf("AdoptLegacySession = %v, %v; want nothing adopted and no error", adopted, err)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Error("the unreadable NautaPy file was removed")
	}
}

func TestAdoptLegacySessionKeepsAnExistingSession(t *testing.T) {
	app := newTestApp(t)
	app.writeSession(t)
	legacy := t.TempDir()
	legacyPath := filepath.Join(legacy, sessionFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"csrfhw": "other", "username": "pepe@nauta.com.cu"}`), 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	if adopted, err := app.AdoptLegacySession(legacy); err != nil || adopted {
		t.Fatalf("AdoptLegacySession = %v, %v; want the NautaGo session kept", adopted, err)
	}
	session, err := nauta.LoadSession(app.sessionPath())
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.Username != "plinkr@nauta.com.cu" {
		t.Errorf("session user = %q, want the one NautaGo already had", session.Username)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Error("the NautaPy session was removed although it was not adopted")
	}
}

func TestProxyAndUpShareTheTimeFormat(t *testing.T) {
	for _, value := range []string{"120", "120s", "2m"} {
		got, err := parseSessionTime(value)
		if err != nil {
			t.Errorf("parseSessionTime(%q): %v", value, err)
			continue
		}
		if got != 2*time.Minute {
			t.Errorf("parseSessionTime(%q) = %s, want 2m", value, got)
		}
	}
}

func TestInfoReportsAQueryFailure(t *testing.T) {
	portal := newFakePortal(t)
	portal.remainingTime = ""
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("info"); code != 0 {
		t.Fatalf("info exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.out.String(), "Usuario Nauta: plinkr@nauta.com.cu") {
		t.Errorf("info printed %q", app.out.String())
	}
}

func TestRunConnectedRunsTheCommandAndClosesTheSession(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("run-connected", "-u", "plinkr@nauta.com.cu", "-p", "s3cret", "echo", "hola"); code != 0 {
		t.Fatalf("run-connected exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.out.String(), "hola") {
		t.Errorf("run-connected printed %q, want the command output", app.out.String())
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("run-connected left the session open")
	}
}

func TestRunConnectedReportsTheCommandExitCode(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	code := app.run("run-connected", "-u", "plinkr", "-p", "s3cret", "sh", "-c", "exit 3")
	if code != 3 {
		t.Errorf("exit code = %d, want the command's own status", code)
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("a failing command left the session open")
	}
}

func TestRunConnectedReportsACommandThatCannotStart(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.err.Reset()

	code := app.run("run-connected", "-u", "plinkr", "-p", "s3cret", "nautago-no-such-command")
	if code != 127 {
		t.Errorf("exit code = %d, want 127", code)
	}
	if !strings.Contains(app.err.String(), "nautago-no-such-command") {
		t.Errorf("stderr = %q, want the reason the command did not start", app.err.String())
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("a command that could not start left the session open")
	}
}

func TestRunConnectedDoesNotRepeatTheCommandFailure(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.err.Reset()

	app.run("run-connected", "-u", "plinkr", "-p", "s3cret", "sh", "-c", "exit 3")
	if strings.Contains(app.err.String(), "nauta run-connected") {
		t.Errorf("stderr = %q, the command's own output should be the only report", app.err.String())
	}
}

func TestRunConnectedRejectsAMissingCommand(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

	if code := app.run("run-connected", "-u", "plinkr", "-p", "s3cret"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.err.String(), "requiere un comando") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestRunConnectedClosesTheSessionAfterAnInterrupt(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	app.Run(ctx, []string{
		"run-connected", "-u", "plinkr@nauta.com.cu", "-p", "s3cret",
		"sleep", "5",
	})

	if len(portal.logouts) == 0 {
		t.Error("the portal was never asked to close the session after the interrupt")
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("an interrupted run-connected left the Nauta session open")
	}
}

func TestEveryDisconnectReleasesTheTunnels(t *testing.T) {
	// NautaPy liberaba los túneles dentro de logout(), así que down y
	// run-connected también lo hacían. Con el túnel arriba el logout no llega.
	for _, command := range [][]string{{"down"}, {"run-connected", "true"}} {
		portal := newFakePortal(t)
		app := newTestApp(t).withPortal(portal)
		app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
		app.writeSession(t)

		stopped := make(chan string, 2)
		app.Tunnels = Tunnels{
			OpenVPNRunning:   func() bool { return true },
			StopOpenVPN:      func(context.Context) error { stopped <- "openvpn"; return nil },
			ProtonVPNRunning: func(context.Context) bool { return false },
			StopProtonVPN:    func(context.Context) error { return nil },
		}
		app.out.Reset()

		app.run(command...)
		select {
		case <-stopped:
		default:
			t.Errorf("%v did not release the openvpn tunnel before logging out", command)
		}
		if len(portal.logouts) == 0 {
			t.Errorf("%v never reached the portal to close the session", command)
		}
	}
}

func TestUpRefusesWhileTheTunnelIsTheConnection(t *testing.T) {
	for _, command := range [][]string{{"up"}, {"run-connected", "true"}} {
		portal := newFakePortal(t)
		portal.online = true
		app := newTestApp(t).withPortal(portal)
		app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

		var up atomic.Bool
		up.Store(true)
		app.Tunnels = tunnelsUp(&up)
		app.out.Reset()

		if code := app.run(command...); code != 1 {
			t.Fatalf("%v exited %d, want 1", command, code)
		}
		if !strings.Contains(app.err.String(), "Hay una conexión activa") {
			t.Errorf("%v stderr = %q", command, app.err.String())
		}
		if !strings.Contains(app.err.String(), "VPN") {
			t.Errorf("%v stderr = %q, want it to name who is giving the connection", command, app.err.String())
		}
		if !up.Load() {
			t.Errorf("%v closed the VPN of someone who is on the internet through it", command)
		}
		if nauta.SessionExists(app.sessionPath()) {
			t.Errorf("%v opened a session through the tunnel", command)
		}
	}
}

func TestUpClosesTheTunnelWhenNothingAnswers(t *testing.T) {
	portal := newFakePortal(t)
	portal.probeDead = true
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

	var up atomic.Bool
	up.Store(true)
	app.Tunnels = tunnelsUp(&up)
	app.out.Reset()

	if code := app.run("up", "--batch"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err.String())
	}
	if up.Load() {
		t.Error("up left the tunnel holding the routing table")
	}
	if !nauta.SessionExists(app.sessionPath()) {
		t.Error("up gave up instead of retrying the login with the tunnel gone")
	}
}

func TestUpReleasesTheTunnelBeforeAskingThePortal(t *testing.T) {
	// El VPN se conecta a mitad de sesión: al cerrar hay que soltarlo antes de
	// consultar el tiempo restante y antes del logout.
	portal := newFakePortal(t)
	var up atomic.Bool
	portal.tunnelUp = up.Load
	portal.afterQuery = func() { up.Store(true) }

	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.Tunnels = tunnelsUp(&up)
	app.out.Reset()

	if code := app.run("up", "-t", "1"); code != 0 {
		t.Fatalf("up exited %d: %s", code, app.err.String())
	}
	if up.Load() {
		t.Error("up left the tunnel it found at shutdown")
	}
	// La primera lectura es de la sesión en marcha; la segunda, de la salida. Las
	// dos tienen que haber llegado al portal.
	if got := strings.Count(app.out.String(), "Tiempo restante: 02:14:24"); got != 2 {
		t.Errorf("up read the remaining time %d times, want both reads to reach the portal: %q", got, app.out.String())
	}
	if len(portal.logouts) == 0 {
		t.Error("up never reached the portal to close the session")
	}
}

func TestInfoReleasesTheTunnelBeforeAskingThePortal(t *testing.T) {
	portal := newFakePortal(t)
	portal.credit = "1.12 CUC"
	var up atomic.Bool
	up.Store(true)
	portal.tunnelUp = up.Load

	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.Tunnels = tunnelsUp(&up)
	app.out.Reset()

	if code := app.run("info", "--credit"); code != 0 {
		t.Fatalf("info --credit exited %d: %s", code, app.err.String())
	}
	if up.Load() {
		t.Error("info left the tunnel holding the routing table")
	}
	for _, want := range []string{"Tiempo restante: 02:14:24", "Crédito: 1.12 CUC"} {
		if !strings.Contains(app.out.String(), want) {
			t.Errorf("info printed %q, want %q", app.out.String(), want)
		}
	}
}

func TestInfoShowsTheCreditWhenAsked(t *testing.T) {
	portal := newFakePortal(t)
	portal.credit = "1.12 CUC"
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("info", "--credit"); code != 0 {
		t.Fatalf("info --credit exited %d: %s", code, app.err)
	}
	if !strings.Contains(app.out.String(), "Crédito: 1.12 CUC") {
		t.Errorf("info printed %q, want the credit", app.out.String())
	}
}

func TestInfoReportsACreditThatCannotBeRead(t *testing.T) {
	portal := newFakePortal(t)
	portal.credit = ""
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	if code := app.run("info", "-c"); code != 0 {
		t.Fatalf("info -c exited %d: %s", code, app.err)
	}
	output := app.out.String()
	if !strings.Contains(output, "Usuario Nauta: plinkr@nauta.com.cu") {
		t.Errorf("info printed %q", output)
	}
	if !strings.Contains(output, "Crédito:") {
		t.Errorf("info printed %q, want the credit line", output)
	}
}

func TestNetworkFailuresAreReportedAsSuch(t *testing.T) {
	app := newTestApp(t)
	app.HTTPClient = &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) {
				return nil, fmt.Errorf("la red no responde")
			},
		},
	}
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.err.Reset()

	if code := app.run("up"); code != 1 {
		t.Fatalf("up exited %d, want 1", code)
	}
	if !strings.Contains(app.err.String(), "Hubo un problema en la red") {
		t.Errorf("stderr = %q, want the network message", app.err.String())
	}
	if strings.Contains(app.err.String(), "s3cret") {
		t.Error("the failure message leaked the password")
	}
}

func TestUpClosesTheSessionWhenTheHistoryFails(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")

	if err := app.Store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	if code := app.run("up", "-t", "1"); code != 1 {
		t.Fatalf("up exited %d, want 1", code)
	}
	if _, err := statFile(app.sessionPath()); err == nil {
		t.Error("up left the session open after failing to record it")
	}
}

func TestUpStopsWhenAnotherTerminalDisconnects(t *testing.T) {
	portal := newFakePortal(t)
	app := newTestApp(t).withPortal(portal)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	done := make(chan int, 1)
	go func() { done <- app.run("up", "-t", "60") }()

	path := app.sessionPath()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := statFile(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("up never established a session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove the session: %v", err)
	}

	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("up exited %d, want 0: %s", code, app.err.String())
		}
		if !strings.Contains(app.out.String(), "Sesión cerrada con éxito") {
			t.Errorf("up printed %q", app.out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("up did not notice the disconnect")
	}
}
