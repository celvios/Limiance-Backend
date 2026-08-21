// Package envelope encrypts short-lived secrets carried in durable events.
package envelope

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
)

var ErrKeyRequired = errors.New("verification encryption key must be base64-encoded 32 bytes")

func Seal(encodedKey, plaintext string) (string, error) {
	key, err := decodeKey(encodedKey)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, []byte(plaintext), nil)...)), nil
}

func Open(encodedKey, sealed string) (string, error) {
	key, err := decodeKey(encodedKey)
	if err != nil {
		return "", err
	}
	body, err := base64.RawStdEncoding.DecodeString(sealed)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(body) < gcm.NonceSize() {
		return "", errors.New("sealed value is too short")
	}
	plain, err := gcm.Open(nil, body[:gcm.NonceSize()], body[gcm.NonceSize():], nil)
	return string(plain), err
}

func decodeKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, ErrKeyRequired
	}
	return key, nil
}
