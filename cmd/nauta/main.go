// Command nauta es la CLI de NautaGo.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/plinkr/nautago/internal/cli"
	"github.com/plinkr/nautago/internal/storage"
)

const legacyDirName = "nautapy"

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := openStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer store.Close()

	app := cli.New(store)
	reportLegacySession(app)
	return app.Run(ctx, os.Args[1:])
}

// Un fallo no detiene el comando: justo `nauta down` puede necesitar correr.
func reportLegacySession(app *cli.App) {
	adopted, err := app.AdoptLegacySession(legacyDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "No se pudo traer la sesión abierta de NautaPy: %v\n", err)
		return
	}
	if adopted {
		fmt.Fprintln(os.Stderr,
			"Se trajo la sesión que NautaPy tenía abierta; ahora se cierra con 'nauta down'.")
	}
}

func openStore() (*storage.Store, error) {
	store, err := storage.Open()
	if err != nil {
		return nil, err
	}
	reportLegacyImport(store)
	return store, nil
}

// La importación corre antes de cualquier comando, así que un fallo se informa en
// vez de devolverse: un archivo heredado ilegible no puede impedir que
// `nauta down` o `nauta users list` funcionen.
func reportLegacyImport(store *storage.Store) {
	report, err := store.ImportLegacy(legacyDir())
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"No se pudieron importar los datos de NautaPy: %v\n"+
				"Sus datos originales no fueron modificados; NautaGo sigue funcionando.\n", err)
		return
	}

	if report.Any() {
		fmt.Fprintf(os.Stderr,
			"Se importaron %d usuario(s) y %d conexión(es) de NautaPy.\n"+
				"NautaGo guarda su copia de las contraseñas cifradas, y NautaPy conserva la suya sin cambios.\n"+
				"La importación es única: las conexiones que siga haciendo en NautaPy no aparecerán aquí.\n",
			report.Users, report.Connections)
	}
	if report.Skipped > 0 {
		fmt.Fprintf(os.Stderr,
			"Se descartaron %d conexión(es) de NautaPy porque tenían una fecha ilegible.\n", report.Skipped)
	}
	if report.Pending {
		fmt.Fprintln(os.Stderr,
			"NautaGo ya tenía datos guardados, así que no importó nada de NautaPy. "+
				"Mover o renombrar los archivos de NautaGo permite que la importación se intente de nuevo.")
	}
}

func legacyDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "share", legacyDirName)
}
