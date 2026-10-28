// Package nauta implementa el protocolo del portal cautivo Nauta. URLs, campos de
// formulario, cookies y HTML no salen de este paquete.
package nauta

import (
	"errors"
	"fmt"
)

var (
	ErrAlreadyLoggedIn = errors.New("Hay una sesión abierta") //nolint:staticcheck // ST1005: mensaje visible heredado de NautaPy.
	// ErrAlreadyConnected: hay internet, pero no por una sesión que este programa
	// pueda cerrar.
	ErrAlreadyConnected = errors.New("Hay una conexión activa")      //nolint:staticcheck // ST1005: mensaje visible heredado de NautaPy.
	ErrNoSession        = errors.New("No hay ninguna sesión activa") //nolint:staticcheck // ST1005: mensaje visible heredado de NautaPy.
)

// NetworkError separa los fallos de transporte de los rechazos del portal: solo
// los primeros se reintentan, y la CLI los presenta de forma distinta.
type NetworkError struct {
	Err error
}

func (e *NetworkError) Error() string { return e.Err.Error() }
func (e *NetworkError) Unwrap() error { return e.Err }

func networkErrorf(op string, err error) error {
	return &NetworkError{Err: fmt.Errorf("%s: %w", op, err)}
}

func portalErrorf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}
