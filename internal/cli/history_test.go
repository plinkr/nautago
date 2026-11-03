package cli

import (
	"strings"
	"testing"
	"time"
)

func TestListConnectionsShowsTheCurrentMonth(t *testing.T) {
	app := newTestApp(t)
	now := fixClockMidMonth(app)
	save(app, "plinkr@nauta.com.cu", now.AddDate(0, 0, -2))
	save(app, "plinkr@nauta.com.cu", now.AddDate(0, -1, 0))
	app.out.Reset()

	if code := app.run("--list-conn"); code != 0 {
		t.Fatalf("--list-conn exited %d: %s", code, app.err)
	}
	output := app.out.String()
	if !strings.Contains(output, "Usuario") || !strings.Contains(output, "Fecha inicio sesión") {
		t.Errorf("the table header is missing: %q", output)
	}
	if strings.Contains(output, now.AddDate(0, -1, 0).Format(connectionTimeLayout)) {
		t.Errorf("the listing included a connection from another month: %q", output)
	}
	if !strings.Contains(output, now.AddDate(0, 0, -2).Format(connectionTimeLayout)) {
		t.Errorf("the listing missed the current month connection: %q", output)
	}
}

func TestListConnectionsShowsOpenConnectionsAsNotAvailable(t *testing.T) {
	app := newTestApp(t)
	if err := app.Store.SaveLogin("plinkr@nauta.com.cu", time.Now()); err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}
	app.out.Reset()

	app.run("--list-conn")
	if !strings.Contains(app.out.String(), "N/A") {
		t.Errorf("an open connection was not reported as N/A: %q", app.out.String())
	}
}

func TestListConnectionsFilters(t *testing.T) {
	app := newTestApp(t)
	now := fixClockMidMonth(app)
	thisMonth := save(app, "plinkr@nauta.com.cu", now)
	lastMonth := save(app, "plinkr@nauta.com.cu", now.AddDate(0, -1, 0))

	app.out.Reset()
	app.run("--list-conn", "--last-month")
	output := app.out.String()
	if !strings.Contains(output, lastMonth.Format(connectionTimeLayout)) {
		t.Errorf("--last-month missed its connection: %q", output)
	}
	if strings.Contains(output, thisMonth.Format(connectionTimeLayout)) {
		t.Errorf("--last-month included a connection from this month: %q", output)
	}

	app.out.Reset()
	app.run("--list-conn", "--all-conn")
	output = app.out.String()
	if !strings.Contains(output, thisMonth.Format(connectionTimeLayout)) ||
		!strings.Contains(output, lastMonth.Format(connectionTimeLayout)) {
		t.Errorf("--all-conn did not show every month: %q", output)
	}
}

func TestMonthFiltersRequireTheListing(t *testing.T) {
	app := newTestApp(t)
	for _, flag := range []string{"--last-month", "--all-conn", "-lm", "-ac"} {
		if code := app.run(flag); code != 1 {
			t.Errorf("%s exited %d, want 1", flag, code)
		}
		if !strings.Contains(app.err.String(), "requiere --list-conn") {
			t.Errorf("%s stderr = %q", flag, app.err.String())
		}
		app.err.Reset()
	}
}

func TestListConnectionsWithoutHistory(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("--list-conn"); code != 0 {
		t.Fatalf("--list-conn exited %d", code)
	}
	if got := app.out.String(); got != "No se encontraron conexiones.\n" {
		t.Errorf("printed %q", got)
	}
}

func TestResumeConnectionsGroupsByUserAndMonth(t *testing.T) {
	app := newTestApp(t)
	base := time.Now()
	start := time.Date(base.Year(), base.Month(), 3, 10, 0, 0, 0, time.Local)
	saveRange(app, "plinkr@nauta.com.cu", start, start.Add(90*time.Minute))
	saveRange(app, "plinkr@nauta.com.cu", start.AddDate(0, -1, 0), start.AddDate(0, -1, 0).Add(30*time.Minute))
	saveRange(app, "otro@nauta.com.cu", start, start.Add(2*time.Hour))
	app.out.Reset()

	if code := app.run("--resume-conn"); code != 0 {
		t.Fatalf("--resume-conn exited %d: %s", code, app.err)
	}
	output := app.out.String()
	if !strings.Contains(output, "Cantidad de horas") {
		t.Errorf("the summary header is missing: %q", output)
	}
	if !strings.Contains(output, "1 hora 30 minutos") {
		t.Errorf("the summary is missing the shorter connection: %q", output)
	}
	if !strings.Contains(output, "2 horas") {
		t.Errorf("the summary is missing the longer connection: %q", output)
	}
	if !strings.Contains(output, "30 minutos") {
		t.Errorf("the summary is missing the short connection: %q", output)
	}

	first := strings.Index(output, base.Format("January 2006"))
	previous := strings.Index(output, base.AddDate(0, -1, 0).Format("January 2006"))
	if first < 0 || previous < 0 || first > previous {
		t.Errorf("months are not ordered from the most recent: %q", output)
	}
}

func TestResumeConnectionsIgnoresOpenConnections(t *testing.T) {
	app := newTestApp(t)
	if err := app.Store.SaveLogin("plinkr@nauta.com.cu", time.Now()); err != nil {
		t.Fatalf("SaveLogin: %v", err)
	}
	app.out.Reset()

	if code := app.run("--resume-conn"); code != 0 {
		t.Fatalf("--resume-conn exited %d", code)
	}
	if got := app.out.String(); got != "No se encontraron conexiones válidas.\n" {
		t.Errorf("printed %q", got)
	}
}

func TestListConnectionsCanBeFollowedByTheSummary(t *testing.T) {
	app := newTestApp(t)
	start := time.Date(time.Now().Year(), time.Now().Month(), 3, 10, 0, 0, 0, time.Local)
	saveRange(app, "plinkr@nauta.com.cu", start, start.Add(time.Hour))
	app.out.Reset()

	if code := app.run("-lc", "-rc"); code != 0 {
		t.Fatalf("-lc -rc exited %d: %s", code, app.err)
	}
	output := app.out.String()
	if !strings.Contains(output, "Fecha inicio sesión") {
		t.Errorf("the listing is missing: %q", output)
	}
	if !strings.Contains(output, "Cantidad de horas") {
		t.Errorf("the summary is missing: %q", output)
	}
}

func TestVersion(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = "v0.9.0"
	app := newTestApp(t)
	if code := app.run("--version"); code != 0 {
		t.Fatalf("--version exited %d", code)
	}
	if got := app.out.String(); got != "nauta v0.9.0\n" {
		t.Errorf("--version printed %q", got)
	}
}

func TestVersionWithoutLdflags(t *testing.T) {
	saved := version
	t.Cleanup(func() { version = saved })

	version = ""
	if got := buildVersion(); got == "" {
		t.Error("buildVersion returned an empty version")
	}
}

func TestBareInvocationShowsUsageAndFails(t *testing.T) {
	app := newTestApp(t)
	if code := app.run(); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	for _, want := range []string{"up", "down", "info", "is-online", "is-logged-in", "run-connected", "users"} {
		if !strings.Contains(app.out.String(), want) {
			t.Errorf("usage does not mention %q: %q", want, app.out.String())
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("teleport"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.err.String(), "subcomando desconocido: teleport") {
		t.Errorf("stderr = %q", app.err.String())
	}
}

func TestUnknownFlag(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("--nope"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestCommandHelp(t *testing.T) {
	app := newTestApp(t)
	if code := app.run("up", "-h"); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if !strings.Contains(app.out.String(), "nauta up") {
		t.Errorf("usage was not printed: %q", app.out.String())
	}
}

func TestOptionsMayFollowPositionalArguments(t *testing.T) {
	app := newTestApp(t)
	app.run("users", "add", "plinkr@nauta.com.cu", "s3cret")
	app.out.Reset()

	// NautaPy aceptaba opciones después de los posicionales.
	if code := app.run("info", "plinkr", "s3cret", "--no-such-option"); code != 1 {
		t.Errorf("an unknown option was accepted, exit %d", code)
	}
}

func TestSessionTimeParsing(t *testing.T) {
	cases := []struct {
		value string
		want  time.Duration
	}{
		{"", 0},
		{"60", time.Minute},
		{"1h", time.Hour},
		{"2H", 2 * time.Hour},
		{"10m", 10 * time.Minute},
		{"30M", 30 * time.Minute},
		{"0", 0},
	}
	for _, c := range cases {
		got, err := parseSessionTime(c.value)
		if err != nil {
			t.Errorf("parseSessionTime(%q) returned %v", c.value, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSessionTime(%q) = %s, want %s", c.value, got, c.want)
		}
	}
}

func TestSessionTimeRejectsNonsense(t *testing.T) {
	for _, value := range []string{"abc", "1x", "-5", "h", "9999999999", "10000000000h"} {
		if _, err := parseSessionTime(value); err == nil {
			t.Errorf("parseSessionTime(%q) accepted an invalid value", value)
		}
	}
}

func TestClockFormat(t *testing.T) {
	cases := map[time.Duration]string{
		0:                            "00:00:00",
		time.Second:                  "00:00:01",
		90 * time.Second:             "00:01:30",
		time.Hour:                    "01:00:00",
		50*time.Hour + 3*time.Minute: "50:03:00",
		-5 * time.Second:             "00:00:00",
	}
	for value, want := range cases {
		if got := clock(value); got != want {
			t.Errorf("clock(%s) = %q, want %q", value, got, want)
		}
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) != "Sí" || yesNo(false) != "No" {
		t.Error("yesNo does not answer in the language of the CLI")
	}
}

func TestRenderTable(t *testing.T) {
	table := renderTable(
		[]string{"Usuario", "Mes"},
		[][]string{
			{"a@nauta.com.cu", "Septiembre 2026"},
			{"b", "Octubre"},
		},
	)
	lines := strings.Split(table, "\n")
	if len(lines) != 6 {
		t.Fatalf("table has %d lines, want 6:\n%s", len(lines), table)
	}
	for i, line := range lines {
		if i%2 == 0 && !strings.HasPrefix(line, "|") {
			t.Errorf("line %d (%q) is not a row", i, line)
		}
		if i%2 == 1 && !strings.HasPrefix(line, "+") {
			t.Errorf("line %d (%q) is not a rule", i, line)
		}
	}
	if !strings.Contains(lines[0], "Usuario") || !strings.Contains(lines[0], "Mes") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[2], "a@nauta.com.cu") || !strings.Contains(lines[2], "Septiembre 2026") {
		t.Errorf("first row = %q", lines[2])
	}
	if want := "+" + strings.Repeat("-", len("a@nauta.com.cu")+2) + "+" + strings.Repeat("-", len("Septiembre 2026")+2) + "+"; lines[1] != want {
		t.Errorf("rule = %q, want %q", lines[1], want)
	}
}

// Mediados de mes: restar días o un mes desde aquí no cruza un límite de mes,
// como sí pasaría con la fecha real los primeros días o el día 31.
func fixClockMidMonth(app *testApp) time.Time {
	now := time.Date(2026, time.March, 15, 12, 0, 0, 0, time.Local)
	app.Now = func() time.Time { return now }
	return now
}

func save(app *testApp, user string, start time.Time) time.Time {
	saveRange(app, user, start, start.Add(time.Minute))
	return start
}

func saveRange(app *testApp, user string, start, end time.Time) {
	if err := app.Store.SaveLogin(user, start); err != nil {
		panic(err)
	}
	if err := app.Store.SaveLogout(user, end); err != nil {
		panic(err)
	}
}
