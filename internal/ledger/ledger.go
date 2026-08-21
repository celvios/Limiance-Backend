package ledger

import (
	"errors"
	"fmt"
	"time"
)

var (
	ErrUnbalanced       = errors.New("journal is not balanced")
	ErrInvalidAmount    = errors.New("amount must be positive")
	ErrDuplicateAccount = errors.New("journal contains duplicate account entry")
)

type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

type Posting struct {
	AccountID string
	AssetID   string
	Direction Direction
	Amount    int64 // atomic units; never use float for financial amounts
}

type Journal struct {
	ID             string
	IdempotencyKey string
	ReferenceType  string
	ReferenceID    string
	CreatedAt      time.Time
	Postings       []Posting
}

func (j Journal) Validate() error {
	if j.ID == "" || j.IdempotencyKey == "" || j.ReferenceType == "" || j.ReferenceID == "" {
		return errors.New("journal identity fields are required")
	}
	if len(j.Postings) < 2 {
		return errors.New("journal needs at least two postings")
	}

	totals := map[string]struct{ debit, credit int64 }{}
	seen := map[string]bool{}
	for _, p := range j.Postings {
		if p.AccountID == "" || p.AssetID == "" {
			return errors.New("posting account and asset are required")
		}
		if p.Amount <= 0 {
			return ErrInvalidAmount
		}
		if p.Direction != Debit && p.Direction != Credit {
			return fmt.Errorf("invalid posting direction %q", p.Direction)
		}
		key := p.AccountID + ":" + p.AssetID + ":" + string(p.Direction)
		if seen[key] {
			return ErrDuplicateAccount
		}
		seen[key] = true
		t := totals[p.AssetID]
		if p.Direction == Debit {
			t.debit += p.Amount
		} else {
			t.credit += p.Amount
		}
		totals[p.AssetID] = t
	}
	for asset, total := range totals {
		if total.debit != total.credit {
			return fmt.Errorf("%w for %s", ErrUnbalanced, asset)
		}
	}
	return nil
}
