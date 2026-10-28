package cli

import (
	"flag"
	"fmt"
	"io"
)

func (a *App) printUsage(global *flag.FlagSet) {
	fmt.Fprint(a.Out, `uso: nauta [-h] [-d] [-lc] [-lm] [-ac] [-rc] [--version] {up,down,info,is-online,is-logged-in,run-connected,users,proxy} ...

Cliente y CLI para el portal cautivo Nauta de Cuba.

comandos:
  up              Inicia una sesión Nauta y la mantiene abierta.
  down            Cierra la sesión Nauta activa.
  info            Muestra el usuario Nauta y su tiempo restante.
  is-online       Indica si hay conexión a internet.
  is-logged-in    Indica si hay una sesión Nauta abierta.
  run-connected   Ejecuta un comando con conexión y la cierra al terminar.
  users           Gestiona los usuarios Nauta guardados.
  proxy           Proxy que abre una sesión Nauta bajo demanda.

Opciones:
`)
	global.SetOutput(a.Out)
	global.PrintDefaults()
	global.SetOutput(io.Discard)
}

func (a *App) printText(text string) {
	fmt.Fprint(a.Out, text)
}
