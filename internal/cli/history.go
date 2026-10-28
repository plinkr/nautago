package cli

import (
	"fmt"
	"sort"
	"time"

	"github.com/plinkr/nautago/internal/storage"
)

type historyOptions struct {
	lastMonth bool
	all       bool
	resume    bool
}

const connectionTimeLayout = "2006-01-02 15:04:05"

func (a *App) listConnections(opts historyOptions) error {
	connections, err := a.selectConnections(opts)
	if err != nil {
		return err
	}
	if len(connections) == 0 {
		fmt.Fprintln(a.Out, "No se encontraron conexiones.")
		return nil
	}

	rows := make([][]string, 0, len(connections))
	for _, conn := range connections {
		rows = append(rows, []string{conn.User, formatMoment(&conn.Start), formatMoment(conn.End)})
	}
	fmt.Fprintln(a.Out, renderTable(
		[]string{"Usuario", "Fecha inicio sesión", "Fecha cierre sesión"}, rows))

	if opts.resume {
		return a.resumeConnections()
	}
	return nil
}

func (a *App) selectConnections(opts historyOptions) ([]storage.Connection, error) {
	now := a.Now()
	switch {
	case opts.lastMonth:
		return a.Store.ConnectionsOfPreviousMonth(now)
	case opts.all:
		return a.Store.Connections()
	default:
		return a.Store.ConnectionsOfMonth(now)
	}
}

func (a *App) resumeConnections() error {
	connections, err := a.Store.Connections()
	if err != nil {
		return err
	}
	if len(connections) == 0 {
		fmt.Fprintln(a.Out, "No se encontraron conexiones.")
		return nil
	}

	totals := newMonthlyTotals()
	for _, conn := range connections {
		if conn.End == nil {
			continue
		}
		totals.add(conn)
	}

	rows := totals.rows()
	if len(rows) == 0 {
		fmt.Fprintln(a.Out, "No se encontraron conexiones válidas.")
		return nil
	}

	fmt.Fprintln(a.Out, renderTable([]string{"Usuario", "Mes", "Cantidad de horas"}, rows))
	return nil
}

type monthlyTotals struct {
	order  []string
	byUser map[string]map[time.Time]float64
}

func newMonthlyTotals() *monthlyTotals {
	return &monthlyTotals{byUser: map[string]map[time.Time]float64{}}
}

func (m *monthlyTotals) add(conn storage.Connection) {
	month := time.Date(conn.Start.Year(), conn.Start.Month(), 1, 0, 0, 0, 0, conn.Start.Location())
	months, seen := m.byUser[conn.User]
	if !seen {
		months = map[time.Time]float64{}
		m.byUser[conn.User] = months
		m.order = append(m.order, conn.User)
	}
	months[month] += conn.End.Sub(conn.Start).Hours()
}

func (m *monthlyTotals) rows() [][]string {
	var rows [][]string
	for _, user := range m.order {
		months := m.byUser[user]
		keys := make([]time.Time, 0, len(months))
		for month := range months {
			keys = append(keys, month)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].After(keys[j]) })

		for _, month := range keys {
			rows = append(rows, []string{user, month.Format("January 2006"), describeHours(months[month])})
		}
	}
	return rows
}

func describeHours(hours float64) string {
	whole := int(hours)
	minutes := int((hours - float64(whole)) * 60)

	switch {
	case whole == 0:
		return fmt.Sprintf("%d minutos", minutes)
	case minutes == 0:
		return fmt.Sprintf("%d horas", whole)
	default:
		return fmt.Sprintf("%d %s %d %s", whole, plural(whole, "hora"), minutes, plural(minutes, "minuto"))
	}
}

func plural(amount int, noun string) string {
	if amount > 1 {
		return noun + "s"
	}
	return noun
}

func formatMoment(at *time.Time) string {
	if at == nil {
		return "N/A"
	}
	return at.Format(connectionTimeLayout)
}
