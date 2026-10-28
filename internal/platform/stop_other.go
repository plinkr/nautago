//go:build !unix

package platform

import (
	"context"
	"strings"
)

func stopOpenVPN(ctx context.Context) error {
	_, err := runCommand(ctx, "taskkill", "/IM", "openvpn.exe", "/F")
	return err
}

// Sin esta lista, StopOpenVPN nunca vería el túnel y no haría nada en Windows.
func processNames() []string {
	out, err := runCommand(context.Background(), "tasklist", "/FO", "CSV", "/NH")
	if err != nil {
		return nil
	}

	var names []string
	for _, line := range strings.Split(out, "\n") {
		// Formato: "nombre.exe","pid",...
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, _, found := strings.Cut(line, ",")
		if !found {
			continue
		}
		names = append(names, strings.Trim(strings.TrimSpace(name), `"`))
	}
	return names
}
