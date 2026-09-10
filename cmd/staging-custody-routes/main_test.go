package main

import "testing"

func TestValidateOptionsFailsClosedForMutations(t *testing.T) {
	valid := options{action: "propose", actorEmail: "operator@example.com", asset: "ETH", network: "ethereum_sepolia",
		feeAsset: "ETH", maxFee: "0.01", observationAge: 15, reason: "approved staging route",
		idempotencyKey: "route-1", confirmStaging: 1}
	if err := validateOptions(valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*options){
		"confirmation": func(value *options) { value.confirmStaging = 0 },
		"actor":        func(value *options) { value.actorEmail = "" },
		"asset":        func(value *options) { value.asset = "" },
		"network":      func(value *options) { value.network = "" },
		"fee asset":    func(value *options) { value.feeAsset = "" },
		"max fee":      func(value *options) { value.maxFee = "" },
		"freshness":    func(value *options) { value.observationAge = 31 },
		"reason":       func(value *options) { value.reason = "short" },
		"idempotency":  func(value *options) { value.idempotencyKey = "" },
	} {
		t.Run(name, func(t *testing.T) {
			input := valid
			mutate(&input)
			if err := validateOptions(input); err == nil {
				t.Fatal("unsafe proposal accepted")
			}
		})
	}
}

func TestValidateApprovalAndInspect(t *testing.T) {
	if err := validateOptions(options{action: "inspect"}); err != nil {
		t.Fatal(err)
	}
	approval := options{action: "approve", actorEmail: "approver@example.com", requestID: "request",
		reason: "approve staging route", idempotencyKey: "approval-1", confirmStaging: 1}
	if err := validateOptions(approval); err != nil {
		t.Fatal(err)
	}
	approval.requestID = ""
	if err := validateOptions(approval); err == nil {
		t.Fatal("approval without request accepted")
	}
}
