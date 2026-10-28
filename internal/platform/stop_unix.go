//go:build unix

package platform

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// openvpn corre como root, así que hace falta sudo, igual que en NautaPy.
func stopOpenVPN(ctx context.Context) error {
	_, err := runCommand(ctx, "sudo", "killall", "openvpn")
	return err
}

func processNames() []string {
	if names := procNames(); names != nil {
		return names
	}
	return psNames()
}

func procNames() []string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "comm"))
		if err != nil {
			// El proceso terminó entre listarlo y leerlo.
			continue
		}
		names = append(names, strings.TrimSpace(string(data)))
	}
	return names
}

// Respaldo para Unix sin /proc, como macOS.
func psNames() []string {
	out, err := exec.Command("ps", "-e", "-o", "comm=").Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}
