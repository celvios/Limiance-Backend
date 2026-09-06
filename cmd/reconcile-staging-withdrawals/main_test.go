package main

import "testing"

func TestReconciliationInputAndTerminalAllowlist(t *testing.T) {
	if _, err := parseIDs(" , "); err == nil {
		t.Fatal("empty withdrawal list accepted")
	}
	ids, err := parseIDs("one,two,one")
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	for _, status := range []string{"COMPLETED", "CONFIRMED", "FAILED", "REJECTED", "CANCELLED"} {
		if !terminal(status) {
			t.Fatalf("terminal status rejected: %s", status)
		}
	}
	for _, status := range []string{"", "SUBMITTED", "BROADCASTING", "PENDING_SIGNATURE"} {
		if terminal(status) {
			t.Fatalf("non-terminal status accepted: %s", status)
		}
	}
}
