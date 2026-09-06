// audit-staging-webhooks prints a sanitized, read-only inventory of durable
// Fireblocks receipts that have not been marked processed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/deposits"
	"github.com/limiance/backend/internal/platform/database"
)

type receipt struct {
	ReceiptID             string `json:"receipt_id"`
	ReceivedAt            string `json:"received_at"`
	Parseable             bool   `json:"parseable"`
	ProviderTransactionID string `json:"provider_transaction_id,omitempty"`
	Status                string `json:"status,omitempty"`
	AssetID               string `json:"asset_id,omitempty"`
	TransactionHash       string `json:"transaction_hash,omitempty"`
}

func main() {
	confirm := flag.Bool("confirm-staging", false, "required staging-only safety acknowledgement")
	flag.Parse()
	cfg := config.Load()
	if !*confirm || cfg.Environment != "staging" {
		fatal(fmt.Errorf("staging environment and --confirm-staging are required"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT id::text,received_at::text,payload
		FROM webhook_receipts WHERE provider='fireblocks' AND processed_at IS NULL
		ORDER BY received_at,id`)
	if err != nil {
		fatal(err)
	}
	defer rows.Close()
	encoder := json.NewEncoder(os.Stdout)
	for rows.Next() {
		var item receipt
		var payload []byte
		if err := rows.Scan(&item.ReceiptID, &item.ReceivedAt, &payload); err != nil {
			fatal(err)
		}
		event, parseErr := deposits.ParseFireblocksEvent(payload)
		if parseErr == nil {
			item.Parseable = true
			item.ProviderTransactionID = event.ProviderTransactionID
			item.Status = event.Status
			item.AssetID = event.AssetID
			item.TransactionHash = event.TransactionHash
		}
		if err := encoder.Encode(item); err != nil {
			fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "staging webhook audit failed:", err)
	os.Exit(1)
}
