package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	timeCost uint32 = 3
	memoryKB uint32 = 64 * 1024
	threads  uint8  = 4
	keyLen   uint32 = 32
)

func Hash(raw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(raw), salt, timeCost, memoryKB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memoryKB, timeCost, threads, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func Verify(encoded, raw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash")
	}
	var memory, time uint32
	var parallel uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &parallel); err != nil {
		return false, err
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	actual := argon2.IDKey([]byte(raw), salt, time, memory, parallel, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1, nil
}
