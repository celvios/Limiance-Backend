package marketmaker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTreasuryInventoryPostgresBalancedAndMakerChecked(t *testing.T) {
	dsn := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("LIMIANCE_TEST_DATABASE_URL is not configured")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("market_maker_treasury_%d", time.Now().UTC().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto; CREATE EXTENSION IF NOT EXISTS citext; CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, cleanupErr := root.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); cleanupErr != nil {
			t.Error(cleanupErr)
		}
		root.Close()
	})
	migrations, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil || len(migrations) == 0 {
		t.Fatalf("load migrations: %v", err)
	}
	for _, name := range migrations {
		raw, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	suffix := time.Now().UTC().UnixNano()
	var operator, approver, asset string
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'x','NG','active') RETURNING id::text`, fmt.Sprintf("mm-treasury-operator-%d@test.invalid", suffix)).Scan(&operator); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'x','NG','active') RETURNING id::text`, fmt.Sprintf("mm-treasury-approver-%d@test.invalid", suffix)).Scan(&approver); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,'treasury_operator'),($1,'treasury_approver'),($2,'treasury_approver')`, operator, approver); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `UPDATE assets SET status='enabled' WHERE symbol='USDT' AND network='internal_spot' RETURNING id::text`).Scan(&asset); err != nil {
		t.Fatal(err)
	}
	service := NewTreasuryInventoryService(pool, "staging", nil)
	limitInput := TreasuryLimitInput{LimitUSDTAtomic: "70000000000000", Reason: "approved all-market staging treasury ceiling", IdempotencyKey: "postgres-limit-proposal"}
	limitRequest, err := service.ProposeLimit(ctx, operator, limitInput)
	if err != nil {
		t.Fatal(err)
	}
	limitRetry, err := service.ProposeLimit(ctx, operator, limitInput)
	if err != nil || limitRetry != limitRequest {
		t.Fatalf("limit proposal retry=%+v error=%v want %+v", limitRetry, err, limitRequest)
	}
	limitConflict := limitInput
	limitConflict.LimitUSDTAtomic = "70000000000001"
	if _, err = service.ProposeLimit(ctx, operator, limitConflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("limit proposal conflict error=%v", err)
	}
	if _, err = service.ApproveLimit(ctx, operator, limitRequest.ID, "same-limit-actor"); !errors.Is(err, ErrSameChecker) {
		t.Fatalf("same limit checker error=%v", err)
	}
	limit, err := service.ApproveLimit(ctx, approver, limitRequest.ID, "postgres-limit-approval")
	if err != nil {
		t.Fatal(err)
	}
	if limit.Version != 2 || limit.LimitUSDTAtomic != limitInput.LimitUSDTAtomic || limit.PolicyID == "" {
		t.Fatalf("limit policy=%+v", limit)
	}
	limitApprovalRetry, err := service.ApproveLimit(ctx, approver, limitRequest.ID, "postgres-limit-approval")
	if err != nil || limitApprovalRetry != limit {
		t.Fatalf("limit approval retry=%+v error=%v want %+v", limitApprovalRetry, err, limit)
	}
	proposal := TreasuryInventoryInput{AssetID: asset, AmountAtomic: "250000000", Reason: "bounded all-market staging inventory", IdempotencyKey: "postgres-proposal"}
	request, err := service.Propose(ctx, operator, proposal)
	if err != nil {
		t.Fatal(err)
	}
	retriedRequest, err := service.Propose(ctx, operator, proposal)
	if err != nil || retriedRequest != request {
		t.Fatalf("proposal retry=%+v error=%v want %+v", retriedRequest, err, request)
	}
	conflict := proposal
	conflict.AmountAtomic = "250000001"
	if _, err = service.Propose(ctx, operator, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("proposal conflict error=%v", err)
	}
	if _, err = service.Approve(ctx, operator, request.ID, "same-actor"); err != ErrSameChecker {
		t.Fatalf("same checker error=%v", err)
	}
	grant, err := service.Approve(ctx, approver, request.ID, "postgres-approval")
	if err != nil {
		t.Fatal(err)
	}
	if grant.ValueUSDTAtomic != "25000000000" {
		t.Fatalf("value=%s", grant.ValueUSDTAtomic)
	}
	retry, err := service.Approve(ctx, approver, request.ID, "postgres-approval")
	if err != nil {
		t.Fatal(err)
	}
	if retry != grant || retry.AccountID == "" {
		t.Fatalf("approval retry=%+v want %+v", retry, grant)
	}
	var evidencePolicyID string
	if err = pool.QueryRow(ctx, `SELECT evidence->>'limit_policy_id' FROM staging_market_maker_treasury_grants WHERE request_id=$1`, request.ID).Scan(&evidencePolicyID); err != nil {
		t.Fatal(err)
	}
	if evidencePolicyID != limit.PolicyID {
		t.Fatalf("grant limit policy=%s want %s", evidencePolicyID, limit.PolicyID)
	}
	var postingCount int
	var net string
	if err = pool.QueryRow(ctx, `SELECT count(*)::int,COALESCE(sum(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0)::text FROM postings WHERE journal_id=$1`, grant.JournalID).Scan(&postingCount, &net); err != nil {
		t.Fatal(err)
	}
	if postingCount != 2 || net != "0" {
		t.Fatalf("postings=%d net=%s", postingCount, net)
	}
	var audits, outbox int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events WHERE resource_id=$1),(SELECT count(*) FROM outbox_events WHERE aggregate_id=$1)`, request.ID).Scan(&audits, &outbox); err != nil {
		t.Fatal(err)
	}
	if audits != 2 || outbox != 2 {
		t.Fatalf("audit=%d outbox=%d", audits, outbox)
	}
	overLimit, err := service.Propose(ctx, operator, TreasuryInventoryInput{AssetID: asset, AmountAtomic: "699751000000", Reason: "prove aggregate ceiling is atomic", IdempotencyKey: "postgres-over-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Approve(ctx, approver, overLimit.ID, "postgres-over-limit-approval"); !errors.Is(err, ErrActivationState) {
		t.Fatalf("over-limit approval error=%v", err)
	}
	lowerRequest, err := service.ProposeLimit(ctx, operator, TreasuryLimitInput{LimitUSDTAtomic: "24999999999", Reason: "prove limit cannot fall below issued value", IdempotencyKey: "postgres-lower-limit"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.ApproveLimit(ctx, approver, lowerRequest.ID, "postgres-lower-limit-approval"); !errors.Is(err, ErrActivationState) {
		t.Fatalf("lower-than-issued limit error=%v", err)
	}
	for _, mutation := range []string{
		`UPDATE staging_market_maker_treasury_requests SET reason='mutated history' WHERE id='` + request.ID + `'`,
		`DELETE FROM staging_market_maker_treasury_grants WHERE request_id='` + request.ID + `'`,
		`TRUNCATE staging_market_maker_treasury_grants`,
		`UPDATE staging_market_maker_treasury_limit_requests SET reason='mutated history' WHERE id='` + limitRequest.ID + `'`,
		`DELETE FROM staging_market_maker_treasury_limit_policies WHERE id='` + limit.PolicyID + `'`,
		`TRUNCATE staging_market_maker_treasury_limit_requests`,
	} {
		if _, err = pool.Exec(ctx, mutation); err == nil {
			t.Fatalf("immutable history accepted %q", mutation)
		}
	}
}
