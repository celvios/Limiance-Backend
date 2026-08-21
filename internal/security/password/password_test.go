package password

import "testing"

func TestHashAndVerify(t *testing.T) {
	hash, err := Hash("A-strong-password-2026")
	if err != nil {
		t.Fatal(err)
	}
	match, err := Verify(hash, "A-strong-password-2026")
	if err != nil || !match {
		t.Fatalf("expected valid password, match=%v err=%v", match, err)
	}
	match, err = Verify(hash, "not-the-password")
	if err != nil || match {
		t.Fatalf("expected invalid password, match=%v err=%v", match, err)
	}
}
