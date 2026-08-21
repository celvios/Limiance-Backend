// Package session creates high-entropy opaque session credentials.
package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const tokenBytes = 32

func New() (raw string, hash []byte, err error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, Hash(raw), nil
}

func Hash(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return sum[:]
}
