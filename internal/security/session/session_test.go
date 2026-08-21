package session

import (
	"bytes"
	"testing"
)

func TestNewCreatesDistinctHashableTokens(t *testing.T) {
	first, firstHash, err := New()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || bytes.Equal(firstHash, secondHash) {
		t.Fatal("session tokens must be unique")
	}
	if !bytes.Equal(firstHash, Hash(first)) {
		t.Fatal("stored hash must match raw token")
	}
}
