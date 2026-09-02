package main

import "testing"

func TestIdentitySeparation(t *testing.T) {
	if err := validateIdentitySeparation(operatorEmail, approverEmail, internalEmail); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][3]string{{"same@example.com", "same@example.com", "internal@example.com"}, {"operator@example.com", "approver@example.com", "operator@example.com"}, {"", "approver@example.com", "internal@example.com"}} {
		if err := validateIdentitySeparation(values[0], values[1], values[2]); err == nil {
			t.Fatalf("accepted non-distinct identities: %#v", values)
		}
	}
}
