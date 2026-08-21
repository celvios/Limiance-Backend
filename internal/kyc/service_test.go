package kyc

import "testing"

func TestAllowedTransitions(t *testing.T) {
	if !allowed(Pending, Approved) {
		t.Fatal("pending approval must be allowed")
	}
	if allowed(Rejected, Approved) {
		t.Fatal("rejected approval must require a new verification flow")
	}
}
