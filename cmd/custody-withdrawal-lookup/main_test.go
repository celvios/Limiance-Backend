package main

import "testing"

func TestParseExternalIDsRejectsEmptyAndDeduplicates(t *testing.T) {
	if _, err := parseExternalIDs(" , "); err == nil {
		t.Fatal("empty external IDs accepted")
	}
	ids, err := parseExternalIDs(" one, two,one ")
	if err != nil || len(ids) != 2 || ids[0] != "one" || ids[1] != "two" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
}
