package main

import "testing"

func TestValidateOptionsFailsClosed(t *testing.T) {
	tests := []options{
		{action: "unknown"},
		{action: "stop", confirmStaging: true, actorEmail: "operator@example.com", reason: "short"},
		{action: "propose-resume", confirmStaging: true, actorEmail: "operator@example.com", reason: "long enough"},
		{action: "propose-funded-dry-run", confirmStaging: true, actorEmail: "operator@example.com", reason: "long enough"},
		{action: "approve-resume", confirmStaging: true, actorEmail: "approver@example.com"},
		{action: "approve-request", confirmStaging: true, actorEmail: "approver@example.com"},
	}
	for _, test := range tests {
		if err := validateOptions(test); err == nil {
			t.Fatalf("validateOptions(%+v) accepted unsafe input", test)
		}
	}
	if err := validateOptions(options{action: "status"}); err != nil {
		t.Fatalf("status rejected: %v", err)
	}
	for _, test := range []options{
		{action: "propose-funded-dry-run", confirmStaging: true, actorEmail: "operator@example.com", reason: "allocate approved inventory", idempotencyKey: "funded-dry-run-1"},
		{action: "approve-request", confirmStaging: true, actorEmail: "approver@example.com", requestID: "request-1"},
	} {
		if err := validateOptions(test); err != nil {
			t.Fatalf("validateOptions(%+v) rejected valid input: %v", test, err)
		}
	}
}

func TestPositiveAmount(t *testing.T) {
	for _, value := range []string{"1", "70000000000000"} {
		if !positiveAmount(value) {
			t.Fatalf("positiveAmount(%q) = false", value)
		}
	}
	for _, value := range []string{"", "0", "01", "-1", "1.0", " 1"} {
		if positiveAmount(value) {
			t.Fatalf("positiveAmount(%q) = true", value)
		}
	}
}
