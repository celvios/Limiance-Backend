package main

import "testing"

func TestOptionsFailClosed(t *testing.T) {
	tests := []options{{action: "bad"}, {action: "propose", confirmStaging: true, actorEmail: "operator", asset: "ETH", amountAtomic: "1", reason: "too short"}, {action: "approve", confirmStaging: true, actorEmail: "approver", requestID: "id"}}
	for _, test := range tests {
		if validateOptions(test) == nil {
			t.Fatalf("unsafe options accepted: %+v", test)
		}
	}
	if err := validateOptions(options{action: "inspect"}); err != nil {
		t.Fatal(err)
	}
}
