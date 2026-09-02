// staging-market-maker-setup prepares only identities and reports existing
// treasury inventory. It cannot fund an account or enable market making.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

const (
	operatorEmail = "toluking001@gmail.com"
	approverEmail = "favourtolu57@gmail.com"
	internalEmail = "internal-market-maker@staging.celvios.site"
)

type identity struct {
	Email     string `json:"email"`
	UserID    string `json:"user_id"`
	Status    string `json:"status"`
	Role      string `json:"role,omitempty"`
	AccountID string `json:"uta_account_id,omitempty"`
}

type treasuryBalance struct {
	AccountID       string `json:"account_id"`
	AccountName     string `json:"account_name"`
	AssetID         string `json:"asset_id"`
	Asset           string `json:"asset"`
	Network         string `json:"network"`
	Decimals        int    `json:"decimals"`
	AvailableAtomic string `json:"available_atomic"`
}

type pair struct {
	Symbol             string `json:"symbol"`
	Base               string `json:"base"`
	Quote              string `json:"quote"`
	PriceScale         int    `json:"price_scale"`
	QuantityScale      int    `json:"quantity_scale"`
	PriceTickAtomic    string `json:"price_tick_atomic"`
	QuantityStepAtomic string `json:"quantity_step_atomic"`
}

type report struct {
	Applied     bool              `json:"applied"`
	Operator    identity          `json:"operator"`
	Approver    identity          `json:"approver"`
	MarketMaker identity          `json:"market_maker"`
	Treasury    []treasuryBalance `json:"funded_system_accounts"`
	Pairs       []pair            `json:"active_pairs"`
}

func main() {
	apply := flag.Bool("apply-identities", false, "grant fixed roles and create the dedicated internal identity")
	flag.Parse()
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal("command is restricted to staging")
	}
	if err := validateIdentitySeparation(operatorEmail, approverEmail, internalEmail); err != nil {
		fatal(err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err.Error())
	}
	defer pool.Close()
	result := report{Applied: *apply, Treasury: []treasuryBalance{}, Pairs: []pair{}}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fatal(err.Error())
	}
	defer tx.Rollback(ctx)
	if err = findHuman(ctx, tx, operatorEmail, "treasury_operator", &result.Operator); err != nil {
		fatal(err.Error())
	}
	if err = findHuman(ctx, tx, approverEmail, "treasury_approver", &result.Approver); err != nil {
		fatal(err.Error())
	}
	if *apply {
		var administrator bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role='platform_administrator')`, result.Operator.UserID).Scan(&administrator); err != nil || !administrator {
			fatal("approved operator must already be a platform administrator")
		}
		if err = grantRole(ctx, tx, result.Operator.UserID, result.Operator.UserID, "treasury_operator"); err != nil {
			fatal(err.Error())
		}
		if err = grantRole(ctx, tx, result.Operator.UserID, result.Approver.UserID, "treasury_approver"); err != nil {
			fatal(err.Error())
		}
		if err = ensureInternalIdentity(ctx, tx, result.Operator.UserID, &result.MarketMaker); err != nil {
			fatal(err.Error())
		}
	} else if err = findInternalIdentity(ctx, tx, &result.MarketMaker); err != nil && err != pgx.ErrNoRows {
		fatal(err.Error())
	}
	if err = loadTreasury(ctx, tx, &result.Treasury); err != nil {
		fatal(err.Error())
	}
	if err = loadPairs(ctx, tx, &result.Pairs); err != nil {
		fatal(err.Error())
	}
	if err = tx.Commit(ctx); err != nil {
		fatal(err.Error())
	}
	if err = json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fatal(err.Error())
	}
}

func validateIdentitySeparation(operator, approver, internal string) error {
	if operator == "" || approver == "" || internal == "" || operator == approver || operator == internal || approver == internal {
		return fmt.Errorf("operator, approver, and internal identity must be distinct")
	}
	return nil
}

func findHuman(ctx context.Context, tx pgx.Tx, email, role string, out *identity) error {
	out.Email, out.Role = email, role
	err := tx.QueryRow(ctx, `SELECT id::text,status FROM users WHERE lower(email)=lower($1)`, email).Scan(&out.UserID, &out.Status)
	if err != nil {
		return fmt.Errorf("required staging user %s not found", email)
	}
	if out.Status != "active" {
		return fmt.Errorf("required staging user %s is not active", email)
	}
	return nil
}

func grantRole(ctx context.Context, tx pgx.Tx, actorID, userID, role string) error {
	tag, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, role)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'admin','admin.role_granted','user',$2,'staging market-maker identity setup',jsonb_build_object('role',$3::text))`, actorID, userID, role); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('admin.role_granted','user',$1,jsonb_build_object('user_id',$1::text,'role',$2::text))`, userID, role)
	return err
}

func ensureInternalIdentity(ctx context.Context, tx pgx.Tx, actorID string, out *identity) error {
	err := findInternalIdentity(ctx, tx, out)
	if err == pgx.ErrNoRows {
		out.Email, out.Status = internalEmail, "active"
		if err = tx.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'internal-identity-no-login','NG','active') RETURNING id::text`, internalEmail).Scan(&out.UserID); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name,status) VALUES($1,'uta','Internal Market Maker UTA','active') RETURNING id::text`, out.UserID).Scan(&out.AccountID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'admin','market_maker.identity_created','user',$2,'dedicated staging market-maker identity',jsonb_build_object('uta_account_id',$3::text))`, actorID, out.UserID, out.AccountID)
		return err
	}
	if err != nil {
		return err
	}
	if out.Status != "active" || out.AccountID == "" {
		return fmt.Errorf("existing internal market-maker identity is not an active single-UTA identity")
	}
	return nil
}

func findInternalIdentity(ctx context.Context, tx pgx.Tx, out *identity) error {
	out.Email = internalEmail
	return tx.QueryRow(ctx, `SELECT u.id::text,u.status,COALESCE((array_agg(a.id) FILTER(WHERE a.kind='uta' AND a.status='active'))[1]::text,'') FROM users u LEFT JOIN accounts a ON a.user_id=u.id WHERE lower(u.email)=lower($1) GROUP BY u.id HAVING count(a.id) FILTER(WHERE a.kind='uta' AND a.status='active')=1`, internalEmail).Scan(&out.UserID, &out.Status, &out.AccountID)
}

func loadTreasury(ctx context.Context, tx pgx.Tx, out *[]treasuryBalance) error {
	rows, err := tx.Query(ctx, `SELECT a.id::text,a.name,s.id::text,s.symbol,s.network,s.decimals,SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END)::text FROM accounts a JOIN postings p ON p.account_id=a.id AND p.bucket='available' JOIN journals j ON j.id=p.journal_id AND j.status='posted' JOIN assets s ON s.id=p.asset_id WHERE a.kind='system' AND a.status='active' GROUP BY a.id,a.name,s.id,s.symbol,s.network,s.decimals HAVING SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END)>0 ORDER BY s.symbol,s.network,a.name`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item treasuryBalance
		if err = rows.Scan(&item.AccountID, &item.AccountName, &item.AssetID, &item.Asset, &item.Network, &item.Decimals, &item.AvailableAtomic); err != nil {
			return err
		}
		*out = append(*out, item)
	}
	return rows.Err()
}

func loadPairs(ctx context.Context, tx pgx.Tx, out *[]pair) error {
	rows, err := tx.Query(ctx, `SELECT p.symbol,b.symbol,q.symbol,p.price_scale,p.quantity_scale,p.price_tick_atomic::text,p.quantity_step_atomic::text FROM trading_pairs p JOIN assets b ON b.id=p.base_asset_id JOIN assets q ON q.id=p.quote_asset_id WHERE p.status='active' ORDER BY p.symbol`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item pair
		if err = rows.Scan(&item.Symbol, &item.Base, &item.Quote, &item.PriceScale, &item.QuantityScale, &item.PriceTickAtomic, &item.QuantityStepAtomic); err != nil {
			return err
		}
		*out = append(*out, item)
	}
	return rows.Err()
}

func fatal(message string) { fmt.Fprintln(os.Stderr, message); os.Exit(1) }
