// Package platform detecta y detiene clientes VPN que retienen la tabla de rutas.
// internal/nauta no depende de él: son integraciones opcionales, no protocolo.
package platform

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// El logout solo llega al portal con las rutas libres, así que hay que esperar,
// pero un proceso atascado no puede colgar el comando.
const processStopGrace = 5 * time.Second

// Compara por fragmento para reconocer el cliente sea cual sea su ruta o sufijo.
func IsProcessRunning(name string) bool {
	needle := strings.ToLower(name)
	for _, candidate := range processNames() {
		if strings.Contains(strings.ToLower(candidate), needle) {
			return true
		}
	}
	return false
}

func StopOpenVPN(ctx context.Context) error {
	if !IsProcessRunning("openvpn") {
		return nil
	}
	if err := stopOpenVPN(ctx); err != nil {
		return err
	}
	return waitProcessGone(ctx, "openvpn", processStopGrace)
}

// ProtonVPN se integra como conexión WireGuard de NetworkManager.
func IsProtonVPNRunning(ctx context.Context) bool {
	out, err := runCommand(ctx, "nmcli", "-t", "-f", "NAME,TYPE", "connection", "show", "--active")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ProtonVPN") && strings.Contains(line, ":wireguard") {
			return true
		}
	}
	return false
}

func StopProtonVPN(ctx context.Context) error {
	if !IsProtonVPNRunning(ctx) {
		return nil
	}
	_, err := runCommand(ctx, "protonvpn", "disconnect")
	return err
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}

func waitProcessGone(ctx context.Context, name string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	for IsProcessRunning(name) {
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return nil
}
