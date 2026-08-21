// Package totp implements the RFC 6238 SHA-1 profile used by mainstream
// authenticator applications. Secrets are generated and encrypted elsewhere;
// this package only derives and checks six-digit time-based codes.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // Required for interoperable RFC 6238 authenticator defaults.
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

const period = 30 * time.Second

func NewSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func Validate(secret, code string, now time.Time) bool {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	for offset := int64(-1); offset <= 1; offset++ {
		if hmac.Equal([]byte(Code(secret, now.Add(time.Duration(offset)*period))), []byte(code)) {
			return true
		}
	}
	return false
}

func Code(secret string, now time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return ""
	}
	counter := uint64(now.UTC().Unix() / int64(period/time.Second))
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, counter)
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message)
	digest := mac.Sum(nil)
	offset := int(digest[len(digest)-1] & 0x0f)
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}
