package envelope

import "testing"

func TestSealAndOpen(t *testing.T) {
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	sealed, err := Seal(key, "123456")
	if err != nil || sealed == "123456" {
		t.Fatalf("seal failed: %q, %v", sealed, err)
	}
	opened, err := Open(key, sealed)
	if err != nil || opened != "123456" {
		t.Fatalf("open failed: %q, %v", opened, err)
	}
}
