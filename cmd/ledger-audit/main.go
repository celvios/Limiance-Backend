package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

type accountBalance struct {
	AccountID string `json:"account_id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Asset     string `json:"asset"`
	Network   string `json:"network"`
	Bucket    string `json:"bucket"`
	Balance   string `json:"balance_atomic"`
}

type movement struct {
	PostingID     int64  `json:"posting_id"`
	OccurredAt    string `json:"occurred_at"`
	ReferenceType string `json:"reference_type"`
	ReferenceID   string `json:"reference_id"`
	AccountID     string `json:"account_id"`
	AccountKind   string `json:"account_kind"`
	Asset         string `json:"asset"`
	Network       string `json:"network"`
	Bucket        string `json:"bucket"`
	Direction     string `json:"direction"`
	Amount        string `json:"amount_atomic"`
}

type record struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Asset     string `json:"asset,omitempty"`
	Network   string `json:"network,omitempty"`
	Amount    string `json:"amount_atomic,omitempty"`
	CreatedAt string `json:"created_at"`
}

type audit struct {
	Email               string           `json:"email"`
	UserID              string           `json:"user_id"`
	UID                 int64            `json:"uid"`
	Balances            []accountBalance `json:"balances"`
	Postings            []movement       `json:"postings"`
	Deposits            []record         `json:"deposits"`
	Withdrawals         []record         `json:"withdrawals"`
	Transfers           []record         `json:"transfers"`
	Conversions         []record         `json:"conversions"`
	WebhookReceipts     int              `json:"webhook_receipts"`
	UnprocessedReceipts int              `json:"unprocessed_webhook_receipts"`
}

func main() {
	email := flag.String("email", "", "user email to audit")
	flag.Parse()
	if *email == "" {
		log.Fatal("--email is required")
	}

	ctx := context.Background()
	pool, err := database.Open(ctx, config.Load().DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	result := audit{Email: *email, Balances: []accountBalance{}, Postings: []movement{}, Deposits: []record{}, Withdrawals: []record{}, Transfers: []record{}, Conversions: []record{}}
	if err := pool.QueryRow(ctx, `SELECT id::text, uid FROM users WHERE email=$1`, *email).Scan(&result.UserID, &result.UID); err != nil {
		log.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
		SELECT a.id::text,a.kind::text,a.name,asset.symbol,asset.network,p.bucket::text,
			COALESCE(SUM(CASE WHEN p.direction='credit' THEN p.amount_atomic ELSE -p.amount_atomic END),0)::text
		FROM accounts a
		JOIN postings p ON p.account_id=a.id
		JOIN journals j ON j.id=p.journal_id AND j.status='posted'
		JOIN assets asset ON asset.id=p.asset_id
		WHERE a.user_id=$1
		GROUP BY a.id,a.kind,a.name,asset.symbol,asset.network,p.bucket
		ORDER BY a.kind,asset.symbol,asset.network,p.bucket`, result.UserID)
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		var item accountBalance
		if err := rows.Scan(&item.AccountID, &item.Kind, &item.Name, &item.Asset, &item.Network, &item.Bucket, &item.Balance); err != nil {
			log.Fatal(err)
		}
		result.Balances = append(result.Balances, item)
	}
	rows.Close()

	rows, err = pool.Query(ctx, `
		SELECT p.id,j.created_at::text,j.reference_type,j.reference_id,a.id::text,a.kind::text,
			asset.symbol,asset.network,p.bucket::text,p.direction,p.amount_atomic::text
		FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted'
		JOIN accounts a ON a.id=p.account_id JOIN assets asset ON asset.id=p.asset_id
		WHERE a.user_id=$1 ORDER BY p.id`, result.UserID)
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		var item movement
		if err := rows.Scan(&item.PostingID, &item.OccurredAt, &item.ReferenceType, &item.ReferenceID, &item.AccountID, &item.AccountKind, &item.Asset, &item.Network, &item.Bucket, &item.Direction, &item.Amount); err != nil {
			log.Fatal(err)
		}
		result.Postings = append(result.Postings, item)
	}
	rows.Close()

	queryRecords := func(query string, args ...any) []record {
		items := []record{}
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			log.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var item record
			if err := rows.Scan(&item.ID, &item.Status, &item.Asset, &item.Network, &item.Amount, &item.CreatedAt); err != nil {
				log.Fatal(err)
			}
			items = append(items, item)
		}
		return items
	}
	result.Deposits = queryRecords(`SELECT d.id::text,d.status,a.symbol,a.network,d.amount_atomic::text,d.first_seen_at::text FROM deposits d JOIN assets a ON a.id=d.asset_id WHERE d.user_id=$1 ORDER BY d.first_seen_at`, result.UserID)
	result.Withdrawals = queryRecords(`SELECT w.id::text,w.status,a.symbol,a.network,w.amount_atomic::text,w.created_at::text FROM withdrawals w JOIN assets a ON a.id=w.asset_id WHERE w.user_id=$1 ORDER BY w.created_at`, result.UserID)
	result.Transfers = queryRecords(`SELECT t.id::text,t.status,a.symbol,a.network,t.amount_atomic::text,t.created_at::text FROM transfers t JOIN accounts source ON source.id=t.source_account_id JOIN assets a ON a.id=t.asset_id WHERE source.user_id=$1 ORDER BY t.created_at`, result.UserID)
	result.Conversions = queryRecords(`SELECT c.id::text,c.status,from_asset.symbol,from_asset.network,q.input_amount_atomic::text,c.created_at::text FROM conversions c JOIN conversion_quotes q ON q.id=c.quote_id JOIN assets from_asset ON from_asset.id=q.from_asset_id WHERE c.user_id=$1 ORDER BY c.created_at`, result.UserID)
	if err := pool.QueryRow(ctx, `SELECT count(*)::int,count(*) FILTER (WHERE processed_at IS NULL)::int FROM webhook_receipts WHERE provider='fireblocks'`).Scan(&result.WebhookReceipts, &result.UnprocessedReceipts); err != nil {
		log.Fatal(err)
	}

	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
