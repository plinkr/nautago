package storage

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Valores tomados de una base real de NautaPy: protegen contra regresiones del
// formato.
func TestB85DecodeAgainstFixtures(t *testing.T) {
	cases := map[string]string{
		"OF(pTWiT@^I59alF#":    "K@tre03081971",
		"Lt$)bVqtD7GB7bXDnm(4": "Caliban*2018*CIM",
		"OE+mXb#8hzN^1":        "K7i5unz4Jk",
		"P*-hKX?SXNa(4":        "PWmTixjurw",
		"RBv*0Z)|TkF)=DCDg":    "Tortolo811***",
	}

	for encoded, want := range cases {
		got, err := b85Decode(encoded)
		if err != nil {
			t.Fatalf("b85Decode(%q) returned error: %v", encoded, err)
		}
		if got != want {
			t.Errorf("b85Decode(%q) = %q, want %q", encoded, got, want)
		}
	}
}

// El resultado debe ser la entrada original, sin el relleno NUL del codificador.
func TestB85DecodeRoundTrip(t *testing.T) {
	seeds := [][]byte{
		{}, {0x00}, {0x01}, {0xff}, {0xff, 0xff}, {0x01, 0x61},
		{'a'}, {'a', 'b'}, {'a', 'b', 'c'}, {'a', 'b', 'c', 'd'},
		[]byte("K@tre03081971"), []byte("Caliban*2018*CIM"), []byte("contraseña"),
	}

	source := rand.New(rand.NewSource(20260925))
	for i := 0; i < 400; i++ {
		length := source.Intn(24)
		blob := make([]byte, length)
		for j := range blob {
			blob[j] = byte(source.Intn(256))
		}
		seeds = append(seeds, blob)
	}

	for _, seed := range seeds {
		encoded := b85EncodeReference(seed)
		got, err := b85Decode(encoded)
		if err != nil {
			t.Fatalf("b85Decode(%q) returned error: %v", encoded, err)
		}
		if want := string(trimNul(seed)); got != want {
			t.Errorf("round trip of %q via %q = %q, want %q", seed, encoded, got, want)
		}
	}
}

func TestB85DecodeRejectsInvalidCharacters(t *testing.T) {
	for _, encoded := range []string{"abcde\"", "abcde ", "abcde,", "abcde\\"} {
		if _, err := b85Decode(encoded); err == nil {
			t.Errorf("b85Decode(%q) accepted an invalid character", encoded)
		}
	}
}

// Réplica de base64.b85encode de Python; solo se usa para generar entradas.
func b85EncodeReference(data []byte) string {
	var out strings.Builder
	for i := 0; i < len(data); i += 4 {
		group := make([]byte, 4)
		copy(group, data[i:min(i+4, len(data))])

		var value uint64
		for _, b := range group {
			value = value<<8 | uint64(b)
		}

		digits := make([]byte, 5)
		for j := 4; j >= 0; j-- {
			digits[j] = b85Alphabet[value%85]
			value /= 85
		}
		out.Write(digits[:len(group)+1])
	}
	return out.String()
}

func TestKeyringSealsAndOpens(t *testing.T) {
	dir := t.TempDir()
	keyring, err := openKeyring(filepath.Join(dir, "credentials.key"))
	if err != nil {
		t.Fatalf("openKeyring: %v", err)
	}

	sealed, err := keyring.seal("s3cr3t p@ss")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if sealed == "s3cr3t p@ss" || !strings.HasPrefix(sealed, cipherPrefix) {
		t.Fatalf("sealed value %q is not a marked cipher text", sealed)
	}

	opened, err := keyring.open(sealed)
	if err != nil {
		t.Fatalf("open(sealed): %v", err)
	}
	if opened != "s3cr3t p@ss" {
		t.Errorf("open(sealed) = %q, want %q", opened, "s3cr3t p@ss")
	}
}

func TestKeyringReadsLegacyValues(t *testing.T) {
	dir := t.TempDir()
	keyring, err := openKeyring(filepath.Join(dir, "credentials.key"))
	if err != nil {
		t.Fatalf("openKeyring: %v", err)
	}

	// Formato de NautaPy: base85 y sin cipherPrefix.
	plain, err := keyring.open("OF(pTWiT@^I59alF#")
	if err != nil {
		t.Fatalf("open(legacy): %v", err)
	}
	if plain != "K@tre03081971" {
		t.Errorf("open(legacy) = %q, want %q", plain, "K@tre03081971")
	}
}

func TestKeyFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.key")
	if _, err := openKeyring(path); err != nil {
		t.Fatalf("openKeyring: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != fileMode {
		t.Errorf("key permissions = %o, want %o", perm, fileMode)
	}
}

func TestKeyringRejectsForeignKey(t *testing.T) {
	first, err := openKeyring(filepath.Join(t.TempDir(), "credentials.key"))
	if err != nil {
		t.Fatalf("openKeyring: %v", err)
	}
	sealed, err := first.seal("value")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	second, err := openKeyring(filepath.Join(t.TempDir(), "credentials.key"))
	if err != nil {
		t.Fatalf("openKeyring: %v", err)
	}
	if _, err := second.open(sealed); err == nil {
		t.Error("a value sealed with a different key was accepted")
	}
}
