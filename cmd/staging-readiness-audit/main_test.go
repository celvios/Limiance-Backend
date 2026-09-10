package main

import "testing"

func TestCommandCessationRequiresObservedStopAndNoCommandsBeforeResume(t *testing.T) {
	tests := []struct {
		name    string
		control marketMakerControl
		want    bool
	}{
		{name: "verified", control: marketMakerControl{LatestStopAt: "2026-09-01T00:00:00Z"}, want: true},
		{name: "no stop evidence", control: marketMakerControl{}, want: false},
		{name: "command during stop", control: marketMakerControl{LatestStopAt: "2026-09-01T00:00:00Z", CommandsDuringStop: 1}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := commandCessationVerified(test.control); got != test.want {
				t.Fatalf("commandCessationVerified()=%v want %v", got, test.want)
			}
		})
	}
}
