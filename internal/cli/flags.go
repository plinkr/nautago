package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

type commandFlags struct {
	fs      *flag.FlagSet
	usage   string
	summary string
}

func (a *App) newCommand(name, usage, summary string) *commandFlags {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	// Los errores de análisis vuelven como error; Run decide dónde se muestran.
	fs.SetOutput(io.Discard)
	return &commandFlags{fs: fs, usage: usage, summary: summary}
}

// Las opciones pueden ir antes o después de los posicionales, como en NautaPy
// (`nauta up plinkr --no-log`). El paquete flag se detiene en el primer
// posicional.
func (c *commandFlags) parse(a *App, argv []string) ([]string, error) {
	if err := c.fs.Parse(reorder(c.fs, argv)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			a.printCommandUsage(c)
			return nil, errUsageRequested
		}
		return nil, err
	}
	return c.fs.Args(), nil
}

func (a *App) printCommandUsage(c *commandFlags) {
	fmt.Fprintf(a.Out, "uso: %s\n", c.usage)
	if c.summary != "" {
		fmt.Fprintf(a.Out, "\n%s\n", c.summary)
	}
	if hasFlags(c.fs) {
		fmt.Fprintf(a.Out, "\nOpciones:\n")
		c.fs.SetOutput(a.Out)
		c.fs.PrintDefaults()
		c.fs.SetOutput(io.Discard)
	}
}

// Cada opción arrastra su valor para que ese valor no se tome por un posicional.
func reorder(fs *flag.FlagSet, args []string) []string {
	var options, positionals []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positionals = append(positionals, arg)
			continue
		}

		options = append(options, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") || takesNoValue(fs, name) {
			continue
		}
		if i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return append(options, positionals...)
}

func takesNoValue(fs *flag.FlagSet, name string) bool {
	option := fs.Lookup(name)
	if option == nil {
		// Parse avisa de la opción desconocida; adivinar aquí taparía el error real.
		return false
	}
	boolean, ok := option.Value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

func hasFlags(fs *flag.FlagSet) bool {
	found := false
	fs.VisitAll(func(*flag.Flag) { found = true })
	return found
}

func splitCommand(args []string) (string, []string) {
	if len(args) == 0 {
		return "", nil
	}
	return args[0], args[1:]
}
