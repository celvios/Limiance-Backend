package openapi

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestExchangeV2ContractParsesAndContainsRequiredSurface(t *testing.T) {
	payload, err := os.ReadFile("exchange-v2.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document, err := openapi3.NewLoader().LoadFromData(payload)
	if err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	if err = document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI: %v", err)
	}
	text := string(payload)
	for _, required := range []string{"openapi: 3.1.0", "/orders:", "/market/orderbook/{pair}:", "/market/tickers:", "/market/klines/{pair}:", "/ws:", "X-API-Nonce", "request_id", "Idempotency-Key", "change_bps_24h", "STOP_MARKET", "TAKE_PROFIT_LIMIT"} {
		if !strings.Contains(text, required) {
			t.Fatalf("contract missing %q", required)
		}
	}
}

func TestExchangeV2MoneySchemasAreAtomicStrings(t *testing.T) {
	payload, _ := os.ReadFile("exchange-v2.yaml")
	text := string(payload)
	for _, schema := range []string{"Atomic: { type: string", "SignedAtomic: { type: string"} {
		if !strings.Contains(text, schema) {
			t.Fatalf("money schema is not a string: %s", schema)
		}
	}
	if strings.Contains(text, "format: double") || strings.Contains(text, "format: float") {
		t.Fatal("floating-point schema found")
	}
}
