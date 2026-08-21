package deposits

import "testing"

func TestParseFireblocksEventAndAtomicAmount(t *testing.T) {
	raw := []byte(`{"eventType":"transaction.status.updated","data":{"id":"fb-tx-1","status":"CONFIRMING","operation":"TRANSFER","assetId":"USDT_ETH","destinationAddress":"0xabc","destinationTag":"","txHash":"0xtx","blockchainIndex":"7","numOfConfirmations":3,"blockInfo":{"blockHash":"0xblock","blockHeight":"100"},"amountInfo":{"netAmount":"12.34"}}}`)
	event, err := ParseFireblocksEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if event.ProviderTransactionID != "fb-tx-1" || event.Confirmations != 3 || event.Amount != "12.34" {
		t.Fatalf("unexpected event: %+v", event)
	}
	atomic, err := AtomicAmount(event.Amount, 6)
	if err != nil || atomic != "12340000" {
		t.Fatalf("atomic=%q err=%v", atomic, err)
	}
	if _, err := AtomicAmount("1.0000001", 6); err == nil {
		t.Fatal("expected excess precision rejection")
	}
}
