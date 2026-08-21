package totp

import (
	"testing"
	"time"
)

func TestRFC6238SHA1SixDigits(t *testing.T) {
	// RFC 6238's SHA-1 test key, truncated to six digits by this API.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	now := time.Unix(59, 0)
	if got, want := Code(secret, now), "287082"; got != want {
		t.Fatalf("Code() = %q, want %q", got, want)
	}
	if !Validate(secret, "287082", now) {
		t.Fatal("expected valid code")
	}
}
