package testmoney

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{GlobalLimitUSDTAtomic: "2000000000000", MaxAgeSeconds: 30, MaxDivergenceBPS: 100, Assets: map[string]AssetPolicy{"asset": {Network: "internal_spot", Decimals: 18}}}
}
func testObservations(now time.Time) []Observation {
	return []Observation{{Venue: "one", PriceUSDTAtomic: "250000000000", ObservedAt: now}, {Venue: "two", PriceUSDTAtomic: "250000000000", ObservedAt: now}}
}

func TestExactValuationAndAggregateQuota(t *testing.T) {
	p := testPolicy()
	now := time.Now()
	value, err := p.Value("asset", "4000000000000000000", testObservations(now), now)
	if err != nil || value != TesterLimitUSDTAtomic {
		t.Fatalf("value=%s err=%v", value, err)
	}
	if err = p.CheckQuota(value, "0", "0"); err != nil {
		t.Fatal(err)
	}
	if err = p.CheckQuota(value, "1", "0"); !errors.Is(err, ErrLimit) {
		t.Fatalf("recipient ceiling: %v", err)
	}
	if err = p.CheckQuota(value, "0", "1000000000001"); !errors.Is(err, ErrLimit) {
		t.Fatalf("global ceiling: %v", err)
	}
	if err = p.CheckQuota("1", "999999999999", "0"); err != nil {
		t.Fatal(err)
	}
	if err = p.CheckQuota("1", "1000000000000", "0"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	// Sub-atom value is rounded up, never free; math exceeds int64 internally.
	value, err = p.Value("asset", "1", testObservations(now), now)
	if err != nil || value != "1" {
		t.Fatalf("rounding=%s %v", value, err)
	}
	p.Assets["asset"] = AssetPolicy{Network: "internal_spot", Decimals: 36}
	value, err = p.Value("asset", "4000000000000000000000000000000000000", testObservations(now), now)
	if err != nil || value != TesterLimitUSDTAtomic {
		t.Fatalf("large=%s %v", value, err)
	}
}

func TestReferenceEvidenceFailsClosed(t *testing.T) {
	now := time.Now()
	p := testPolicy()
	cases := map[string]func([]Observation) []Observation{
		"missing":    func(o []Observation) []Observation { return o[:1] },
		"same venue": func(o []Observation) []Observation { o[1].Venue = " ONE "; return o },
		"stale":      func(o []Observation) []Observation { o[0].ObservedAt = now.Add(-31 * time.Second); return o },
		"future":     func(o []Observation) []Observation { o[0].ObservedAt = now.Add(time.Second); return o },
		"zero time":  func(o []Observation) []Observation { o[0].ObservedAt = time.Time{}; return o },
		"divergent":  func(o []Observation) []Observation { o[1].PriceUSDTAtomic = "260000000000"; return o },
		"zero price": func(o []Observation) []Observation { o[1].PriceUSDTAtomic = "0"; return o },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := p.Value("asset", "1", mutate(testObservations(now)), now); !errors.Is(err, ErrReference) {
				t.Fatal(err)
			}
		})
	}
}

func TestPolicyAndAmountsRejectInvalidInputs(t *testing.T) {
	p := testPolicy()
	now := time.Now()
	for _, amount := range []string{"", "0", "-1", "+1", "01", "1.0", "1e18", " 1", strings.Repeat("9", 79)} {
		if _, err := p.Value("asset", amount, testObservations(now), now); !errors.Is(err, ErrInput) {
			t.Fatalf("%q: %v", amount, err)
		}
	}
	if _, err := p.Value("unknown", "1", testObservations(now), now); !errors.Is(err, ErrInput) {
		t.Fatal(err)
	}
	for _, network := range []string{"ethereum", "bitcoin", "mainnet", ""} {
		p.Assets["asset"] = AssetPolicy{Network: network, Decimals: 18}
		if err := p.Validate(); !errors.Is(err, ErrInput) {
			t.Fatalf("%s: %v", network, err)
		}
	}
	if err := (Policy{}).Validate(); err == nil {
		t.Fatal("empty policy accepted")
	}
}

func TestServiceEnvironmentFailsBeforeDatabaseAccess(t *testing.T) {
	for _, environment := range []string{"", "production", "mainnet", "development", "STAGING"} {
		s := NewService(nil, environment, nil)
		if _, err := s.Approve(context.Background(), "actor", "request", "key"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("%s: %v", environment, err)
		}
	}
}
