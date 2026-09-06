package main

import (
	"reflect"
	"testing"
)

func TestMutationConfirmationBoundary(t *testing.T) {
	if requiresConfirmation("inspect") {
		t.Fatal("inspect must remain read-only")
	}
	for _, action := range []string{"prepare", "propose", "approve", "unknown"} {
		if !requiresConfirmation(action) {
			t.Fatalf("%s must require explicit staging confirmation", action)
		}
	}
}

func TestSplitListDoesNotInventDefaults(t *testing.T) {
	if splitList(" ") != nil {
		t.Fatal("empty input must remain empty")
	}
	if got := splitList("BTC, ETH"); !reflect.DeepEqual(got, []string{"BTC", " ETH"}) {
		t.Fatalf("unexpected split: %#v", got)
	}
}
