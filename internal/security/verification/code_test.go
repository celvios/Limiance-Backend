package verification

import "testing"

func TestCodeHash(t *testing.T) {
	code, err := NewCode()
	if err != nil || len(code) != 6 {
		t.Fatalf("invalid code %q: %v", code, err)
	}
	hash, err := Hash("at-least-thirty-two-characters-long!", code)
	if err != nil || !Equal(hash, hash) {
		t.Fatal("valid code hash was not comparable")
	}
}
