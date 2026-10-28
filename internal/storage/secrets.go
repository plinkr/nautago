package storage

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Un valor sin cipherPrefix viene de NautaPy y está en base85.
const cipherPrefix = "nautago:v1:"

const keySize = 32

// keyring usa una clave local con permisos 0600: es lo más fuerte que ofrece la
// biblioteca estándar sin depender del keychain de cada sistema.
type keyring struct {
	aead cipher.AEAD
}

var errKeyLost = errors.New("no se encontró y hay contraseñas cifradas con una clave anterior; " +
	"no se pueden recuperar, hay que volver a escribir cada contraseña con 'nauta users set-password'")

func (s *Store) sealer() (*keyring, error) {
	if s.keyErr != nil {
		return nil, s.keyErr
	}
	return s.secrets, nil
}

// resetSealer solo crea una clave nueva tras errKeyLost: lo cifrado con la
// anterior ya es irrecuperable. Con cualquier otro problema (un archivo dañado)
// reemplazarla destruiría lo que aún se pudiera rescatar.
func (s *Store) resetSealer() (*keyring, error) {
	if errors.Is(s.keyErr, errKeyLost) {
		secrets, err := openKeyring(s.keyPath)
		if err != nil {
			return nil, err
		}
		s.secrets, s.keyErr = secrets, nil
	}
	return s.sealer()
}

// ensureKeyRecoverable impide generar una clave nueva en silencio cuando aún hay
// contraseñas cifradas con la anterior.
func ensureKeyRecoverable(path string, users *sql.DB) error {
	if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return nil
	}

	var marker int
	err := users.QueryRow("SELECT 1 FROM users WHERE password LIKE ? LIMIT 1", cipherPrefix+"%").Scan(&marker)
	switch {
	case err == nil:
		return fmt.Errorf("credential key %s: %w", path, errKeyLost)
	case errors.Is(err, sql.ErrNoRows):
		return nil
	default:
		return fmt.Errorf("read stored passwords: %w", err)
	}
}

func openKeyring(path string) (*keyring, error) {
	key, err := readOrCreateKey(path)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credential key: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential key: %w", err)
	}
	return &keyring{aead: aead}, nil
}

func readOrCreateKey(path string) ([]byte, error) {
	data, err := readKey(path)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return data, err
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate credential key: %w", err)
	}
	if err := writeKey(path, key); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		// Otro proceso ganó la carrera y puede haber cifrado ya con su clave. Se
		// lee una sola vez: con un symlink roto, reintentar no terminaría nunca.
		data, err := readKey(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("credential key %s: la ruta está ocupada pero no se puede leer "+
				"(¿un enlace simbólico roto?)", path)
		}
		return data, err
	}
	return key, nil
}

// os.ErrNotExist se devuelve sin envolver para que el llamador decida si crearla.
func readKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(data) != keySize {
			return nil, fmt.Errorf("credential key %s: unexpected size %d bytes", path, len(data))
		}
		return data, nil
	case errors.Is(err, os.ErrNotExist):
		return nil, err
	default:
		return nil, fmt.Errorf("read credential key: %w", err)
	}
}

// writeKey publica la clave de forma atómica: una clave truncada dejaría
// ilegibles todas las contraseñas. Usa os.Link y no os.Rename porque el rename
// reemplaza sin avisar, y con dos primeros arranques simultáneos uno perdería lo
// que ya había cifrado. Link falla con ErrExist si otro proceso ganó.
func writeKey(path string, key []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), "credentials-*.key")
	if err != nil {
		return fmt.Errorf("create credential key: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)

	// os.CreateTemp crea con 0600: la clave no queda expuesta ni a medio escribir.
	if _, err := file.Write(key); err != nil {
		file.Close()
		return fmt.Errorf("write credential key: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("write credential key: %w", err)
	}
	if err := os.Link(name, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return err
		}
		// Sin enlaces duros en el sistema de archivos, el rename sigue siendo
		// atómico aunque no detecte la carrera.
		if err := os.Rename(name, path); err != nil {
			return fmt.Errorf("write credential key: %w", err)
		}
	}
	return nil
}

func (k *keyring) seal(plain string) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	sealed := k.aead.Seal(nonce, nonce, []byte(plain), nil)
	return cipherPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

func (k *keyring) open(stored string) (string, error) {
	if len(stored) < len(cipherPrefix) || stored[:len(cipherPrefix)] != cipherPrefix {
		plain, err := b85Decode(stored)
		if err != nil {
			return "", err
		}
		return plain, nil
	}

	raw, err := base64.StdEncoding.DecodeString(stored[len(cipherPrefix):])
	if err != nil {
		return "", fmt.Errorf("decode stored password: %w", err)
	}
	nonceSize := k.aead.NonceSize()
	if len(raw) < nonceSize {
		return "", errors.New("stored password is truncated")
	}
	plainBytes, err := k.aead.Open(nil, raw[:nonceSize], raw[nonceSize:], nil)
	if err != nil {
		// La clave cambió después de cifrar. El mensaje dice cómo salir porque el
		// aviso de clave perdida ya no aparece una vez creada la nueva.
		return "", errors.New("stored password cannot be decrypted with the local credential key; " +
			"vuelva a escribirla con 'nauta users set-password'")
	}
	return string(plainBytes), nil
}
