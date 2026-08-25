// bootstrap-platform-admin grants the first platform administrator in a fresh
// development environment. It refuses to run once any administrator exists;
// subsequent role changes must use the ordinary audited admin API.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

func main() {
	email := flag.String("email", "", "email address for the initial platform administrator")
	confirmStaging := flag.Bool("confirm-staging", false, "explicitly authorize staging bootstrap")
	flag.Parse()
	if strings.TrimSpace(*email) == "" {
		fatal("email is required")
	}
	cfg := config.Load()
	if cfg.Environment != "development" && cfg.Environment != "staging" {
		fatal("bootstrap is restricted to development or staging")
	}
	if cfg.Environment == "staging" {
		if !*confirmStaging || strings.ToLower(strings.TrimSpace(*email)) != "toluking001@gmail.com" {
			fatal("staging bootstrap requires -confirm-staging and the approved administrator email")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
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
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_roles WHERE role='platform_administrator')`).Scan(&exists); err != nil {
		fatal(err.Error())
	}
	if exists {
		fatal("a platform administrator already exists")
	}
	var userID, status string
	if err = tx.QueryRow(ctx, `SELECT id::text,status FROM users WHERE email=$1`, strings.TrimSpace(*email)).Scan(&userID, &status); err != nil {
		fatal("user not found")
	}
	if status != "active" {
		fatal("initial administrator must be an active user")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES ($1,'platform_administrator')`, userID); err != nil {
		fatal(err.Error())
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES ($1,'system','admin.bootstrap_granted','user',$2,'initial development administrator',jsonb_build_object('role','platform_administrator'))`, userID, userID); err != nil {
		fatal(err.Error())
	}
	if err = tx.Commit(ctx); err != nil {
		fatal(err.Error())
	}
	fmt.Println("initial platform administrator granted")
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
