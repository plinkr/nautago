package nauta

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Session es lo que permite que un `nauta down` cierre la sesión abierta por
// otro proceso. Las cookies quedan fuera a propósito: el portal acepta estos
// valores en la propia petición, y una cookie vieja es peor que ninguna. Las
// claves JSON coinciden con NautaPy para poder adoptar su archivo.
type Session struct {
	LoginAction   string `json:"login_action"`
	CSRFHW        string `json:"csrfhw"`
	WlanUserIP    string `json:"wlanuserip"`
	AttributeUUID string `json:"attribute_uuid"`
	Username      string `json:"username"`
}

func LoadSession(path string) (*Session, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoSession
		}
		return nil, fmt.Errorf("read session: %w", err)
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	return &session, nil
}

func SaveSession(path string, session *Session) error {
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	// Escritura atómica: un archivo truncado dejaría al usuario sin forma de
	// cerrar una sesión que sigue facturando.
	file, err := os.CreateTemp(filepath.Dir(path), "nauta-session-*.tmp")
	if err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)

	// os.CreateTemp crea con 0600, así que los tokens no quedan legibles por otros.
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write session: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("write session: %w", err)
	}
	return nil
}

func DisposeSession(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove session: %w", err)
	}
	return nil
}

// AdoptSession mueve en lugar de copiar porque una sesión solo puede tener un
// responsable de cerrarla: una copia en from volvería a importarse tras cada
// `nauta down`.
func AdoptSession(from, path string) (bool, error) {
	if SessionExists(path) {
		return false, nil
	}
	// NautaPy escribe sus cookies en esta misma ruta antes del JSON, así que un
	// archivo ilegible es normal; reportarlo haría fallar todos los comandos.
	session, err := LoadSession(from)
	if err != nil {
		return false, nil
	}
	if err := SaveSession(path, session); err != nil {
		return false, err
	}
	if err := os.Remove(from); err != nil && !os.IsNotExist(err) {
		return true, fmt.Errorf("remove adopted session: %w", err)
	}
	return true, nil
}

// Un archivo ilegible no cuenta como sesión: no sirve para cerrarla, y tomarlo
// por activa bloquearía el `nauta up` que podría reemplazarlo.
func SessionExists(path string) bool {
	session, err := LoadSession(path)
	return err == nil && session != nil
}
