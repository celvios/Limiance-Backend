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

func TestCanStartKYCSession(t *testing.T) {
	if !canStartKYCSession(string(Approved), 1) {
		t.Fatal("approved level-1 users must be able to restart for level 2")
	}
	if canStartKYCSession(string(Approved), 2) {
		t.Fatal("approved level-2 users must be blocked from opening another verification")
	}
}
