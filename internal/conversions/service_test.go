package conversions

import "testing"

func TestQuotedAmountsAvoidsFloatAndAppliesFee(t *testing.T) {
	net, fee, err := quotedAmounts(100000000, "100", 8, 6, true, 0, 25)
	if err != nil || net != 99750000 || fee != 250000 {
		t.Fatalf("net=%d fee=%d err=%v", net, fee, err)
	}
}

func TestQuotedAmountsBuyBaseUsesAsk(t *testing.T) {
	net, fee, err := quotedAmounts(100000000, "20000", 6, 8, false, 0, 0)
	if err != nil || net != 500000 || fee != 0 {
		t.Fatalf("net=%d fee=%d err=%v", net, fee, err)
	}
}
