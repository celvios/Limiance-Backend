package main

import "testing"

func TestValidateOptionsFailsClosed(t *testing.T) {
	tests := []options{
		{action: "unknown"},
		{action: "stop", confirmStaging: true, actorEmail: "operator@example.com", reason: "short"},
		{action: "propose-resume", confirmStaging: true, actorEmail: "operator@example.com", reason: "long enough"},
		{action: "approve-resume", confirmStaging: true, actorEmail: "approver@example.com"},
	}
	for _, test := range tests {
		if err := validateOptions(test); err == nil {
			t.Fatalf("validateOptions(%+v) accepted unsafe input", test)
		}
	}
	if err := validateOptions(options{action: "status"}); err != nil {
		t.Fatalf("status rejected: %v", err)
	}
}
