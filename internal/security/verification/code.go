// Package verification creates and checks short-lived verification codes.
package verification

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
)

var ErrPepperRequired = errors.New("verification code pepper is required")

func NewCode() (string, error) {
	var value uint32
	if err := binary.Read(rand.Reader, binary.BigEndian, &value); err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func Hash(pepper, code string) ([]byte, error) {
	if len(pepper) < 32 {
		return nil, ErrPepperRequired
	}
	mac := hmac.New(sha256.New, []byte(pepper))
	_, _ = mac.Write([]byte(code))
	return mac.Sum(nil), nil
}

func Equal(expected, actual []byte) bool { return subtle.ConstantTimeCompare(expected, actual) == 1 }
