package ledger

import (
	"errors"
	"testing"
	"time"
)

func TestJournalValidate(t *testing.T) {
	journal := Journal{ID: "j_1", IdempotencyKey: "transfer:1", ReferenceType: "transfer", ReferenceID: "t_1", CreatedAt: time.Now(), Postings: []Posting{
		{AccountID: "user:a:available", AssetID: "USDT", Direction: Credit, Amount: 1000000},
		{AccountID: "user:b:available", AssetID: "USDT", Direction: Debit, Amount: 1000000},
	}}
	if err := journal.Validate(); err != nil {
		t.Fatalf("expected valid journal: %v", err)
	}
}

func TestJournalRejectsUnbalancedPostings(t *testing.T) {
	journal := Journal{ID: "j_1", IdempotencyKey: "transfer:1", ReferenceType: "transfer", ReferenceID: "t_1", Postings: []Posting{
		{AccountID: "user:a:available", AssetID: "USDT", Direction: Credit, Amount: 1000000},
		{AccountID: "user:b:available", AssetID: "USDT", Direction: Debit, Amount: 999999},
	}}
	if !errors.Is(journal.Validate(), ErrUnbalanced) {
		t.Fatal("expected unbalanced journal error")
	}
}
