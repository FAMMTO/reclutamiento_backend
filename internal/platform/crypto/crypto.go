// Package crypto provee cifrado simétrico AES-256-GCM para secretos en reposo
// (tokens y credenciales de terceros almacenados en Postgres).
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Encryptor cifra y descifra con AES-256-GCM usando una clave de 32 bytes.
type Encryptor struct {
	key []byte
}

// New crea un Encryptor. key debe ser exactamente 32 bytes (AES-256).
func New(key []byte) (*Encryptor, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("crypto: la clave debe tener exactamente 32 bytes (tiene %d)", len(key))
	}
	k := make([]byte, 32)
	copy(k, key)
	return &Encryptor{key: k}, nil
}

// Encrypt cifra plaintext y devuelve un string base64 con el nonce prepended.
// El resultado es: base64(nonce || ciphertext || tag).
func (e *Encryptor) Encrypt(plaintext []byte) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", fmt.Errorf("crypto: crear cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("crypto: crear GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: generar nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt invierte Encrypt. Devuelve error si el ciphertext fue alterado.
func (e *Encryptor) Decrypt(ciphertext string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("crypto: decodificar base64: %w", err)
	}

	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("crypto: crear cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: crear GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("crypto: ciphertext demasiado corto")
	}

	nonce, ciphertextBytes := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertextBytes, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: descifrar: %w", err)
	}

	return plaintext, nil
}
