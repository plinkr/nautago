package storage

import (
	"errors"
)

// Alfabeto de base64.b85encode de Python, con el que NautaPy guardaba las
// contraseñas.
const b85Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz!#$%&()*+-;<=>?@^_`{|}~"

var b85Values = func() [256]int8 {
	var table [256]int8
	for i := range table {
		table[i] = -1
	}
	for i := 0; i < len(b85Alphabet); i++ {
		table[b85Alphabet[i]] = int8(i)
	}
	return table
}()

const b85Group = 5

func b85Decode(value string) (string, error) {
	decoded := make([]byte, 0, len(value)/b85Group*4)

	for i := 0; i+b85Group <= len(value); i += b85Group {
		group, err := b85Value(value[i : i+b85Group])
		if err != nil {
			return "", err
		}
		decoded = append(decoded, byte(group>>24), byte(group>>16), byte(group>>8), byte(group))
	}

	if tail := len(value) % b85Group; tail > 0 {
		group, err := b85Value(value[len(value)-tail:])
		if err != nil {
			return "", err
		}

		// Los caracteres que faltan son los bajos de un grupo completo, así que el
		// valor se escala de vuelta y se redondea al byte del que salió.
		shift := uint(8 * (b85Group - tail))
		half := uint64(1) << shift
		bytes := tail - 1
		scaled := (group*pow85(b85Group-tail) + half - 1) >> shift
		for i := bytes - 1; i >= 0; i-- {
			decoded = append(decoded, byte(scaled>>(8*i)))
		}
	}

	return string(trimNul(decoded)), nil
}

func b85Value(digits string) (uint64, error) {
	var value uint64
	for i := 0; i < len(digits); i++ {
		digit := b85Values[digits[i]]
		if digit < 0 {
			return 0, errors.New("malformed base85 password: invalid character")
		}
		value = value*85 + uint64(digit)
	}
	return value, nil
}

func pow85(exponent int) uint64 {
	value := uint64(1)
	for i := 0; i < exponent; i++ {
		value *= 85
	}
	return value
}

func trimNul(data []byte) []byte {
	end := len(data)
	for end > 0 && data[end-1] == 0 {
		end--
	}
	return data[:end]
}
