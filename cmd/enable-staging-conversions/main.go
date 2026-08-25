package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

const adminEmail = "toluking001@gmail.com"

func main() {
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal("staging conversion enablement is restricted to APP_ENV=staging")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err.Error())
	}
	defer pool.Close()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fatal(err.Error())
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var userID string
	if err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE email=$1 AND status='active'`, adminEmail).Scan(&userID); err != nil {
		fatal("approved staging administrator not found or inactive")
	}
	var administrator bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, userID).Scan(&administrator); err != nil || !administrator {
		fatal("approved staging administrator role is missing")
	}
	if _, err = tx.Exec(ctx, `UPDATE assets SET status='enabled' WHERE (symbol='SOL' AND network='solana_testnet') OR (symbol='POL' AND network='polygon_amoy')`); err != nil {
		fatal(err.Error())
	}
	if _, err = tx.Exec(ctx, `UPDATE operational_controls SET enabled=true, updated_by=$1, updated_at=now() WHERE control_key='conversions_enabled'`, userID); err != nil {
		fatal(err.Error())
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES ($1,'user','staging.conversions_enabled','operational_control','conversions_enabled','staging test route enablement',jsonb_build_object('routes',jsonb_build_array('SOL/solana_testnet','POL/polygon_amoy')))` , userID); err != nil {
		fatal(err.Error())
	}
	if err = tx.Commit(ctx); err != nil {
		fatal(err.Error())
	}
	fmt.Println("staging conversion routes and control enabled")
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(message))
	os.Exit(1)
}
