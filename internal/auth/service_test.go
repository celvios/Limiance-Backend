package auth

import "testing"

func TestValidLoginIdentifier(t *testing.T) {
	tests := []struct {
		identifier string
		valid      bool
	}{
		{identifier: "user@example.com", valid: true},
		{identifier: "+2348012345678", valid: true},
		{identifier: "+1234567", valid: false},
		{identifier: "08012345678", valid: false},
		{identifier: "+23480abc", valid: false},
	}
	for _, test := range tests {
		if got := validLoginIdentifier(test.identifier); got != test.valid {
			t.Errorf("validLoginIdentifier(%q) = %t, want %t", test.identifier, got, test.valid)
		}
	}
}
