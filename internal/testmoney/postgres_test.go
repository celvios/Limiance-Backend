package testmoney

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	pool                                           *pgxpool.Pool
	service                                        *Service
	ctx                                            context.Context
	maker, checker, user, account, asset, policyID string
}
type fixedReferences struct{}

func TestPostgresPolicyAndAssetChangesDoNotResetTesterQuota(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	in := f.input("full")
	in.AmountAtomic = "4000000000000000000"
	r, err := f.service.Propose(f.ctx, f.maker, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Approve(f.ctx, f.checker, r.ID, "full"); err != nil {
		t.Fatal(err)
	}
	var second string
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES('SECOND_TEST','internal_spot',18,'enabled') RETURNING id::text`).Scan(&second); err != nil {
		t.Fatal(err)
	}
	p := testPolicy()
	p.Assets = map[string]AssetPolicy{second: {Network: "internal_spot", Decimals: 18}}
	raw, _ := json.Marshal(p)
	if err = f.pool.QueryRow(f.ctx, `INSERT INTO test_money_policies(policy) VALUES($1) RETURNING id::text`, raw).Scan(&f.policyID); err != nil {
		t.Fatal(err)
	}
	f.enable(t)
	in = f.input("new-policy")
	in.AssetID = second
	in.AmountAtomic = "1"
	r, err = f.service.Propose(f.ctx, f.maker, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Approve(f.ctx, f.checker, r.ID, "over"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM test_money_grants"); n != 1 {
		t.Fatal(n)
	}
}

func TestPostgresGlobalQuotaIncludesOtherRecipients(t *testing.T) {
	f := databaseFixture(t)
	p := testPolicy()
	p.GlobalLimitUSDTAtomic = "250000000000"
	p.Assets = map[string]AssetPolicy{f.asset: {Network: "internal_spot", Decimals: 18}}
	raw, _ := json.Marshal(p)
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO test_money_policies(policy) VALUES($1) RETURNING id::text`, raw).Scan(&f.policyID); err != nil {
		t.Fatal(err)
	}
	f.enable(t)
	r := f.propose(t, "first")
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "first"); err != nil {
		t.Fatal(err)
	}
	// Make the existing checker a separate eligible tester.
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO test_money_recipients(user_id,enabled) VALUES($1,TRUE)`, f.checker); err != nil {
		t.Fatal(err)
	}
	var account string
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'funding','second tester') RETURNING id::text`, f.checker).Scan(&account); err != nil {
		t.Fatal(err)
	}
	in := f.input("second")
	in.RecipientID = f.checker
	in.AccountID = account
	second, err := f.service.Propose(f.ctx, f.maker, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.service.Approve(f.ctx, f.checker, second.ID, "second"); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}

func (fixedReferences) Observations(context.Context, string) ([]Observation, error) {
	return testObservations(time.Now().UTC()), nil
}

func databaseFixture(t *testing.T) *fixture {
	t.Helper()
	url := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LIMIANCE_TEST_DATABASE_URL is required for PostgreSQL integration evidence")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	// Create actual production tables/migrations in an isolated schema.
	schema := fmt.Sprintf("issuance_%d", time.Now().UnixNano())
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pgcrypto; CREATE EXTENSION IF NOT EXISTS citext; CREATE SCHEMA "+ident); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
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
		if _, err := root.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	files, err := filepath.Glob("../../migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations")
	}
	for _, name := range files {
		raw, e := os.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(raw)); e != nil {
			t.Fatalf("%s: %v", name, e)
		}
	}
	f := &fixture{pool: pool, ctx: ctx}
	for _, dest := range []*string{&f.maker, &f.checker, &f.user} {
		if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status)
  VALUES(gen_random_uuid()::text || '@test.invalid','unused','NG','active') RETURNING id::text`).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,'treasury_operator'),($2,'treasury_approver')`, f.maker, f.checker); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'funding','tester') RETURNING id::text`, f.user).Scan(&f.account); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO assets(symbol,network,decimals,status) VALUES('ISSUANCE_TEST','internal_spot',18,'enabled') RETURNING id::text`).Scan(&f.asset); err != nil {
		t.Fatal(err)
	}
	p := testPolicy()
	p.Assets = map[string]AssetPolicy{f.asset: {Network: "internal_spot", Decimals: 18}}
	raw, _ := json.Marshal(p)
	if err = pool.QueryRow(ctx, `INSERT INTO test_money_policies(policy) VALUES($1) RETURNING id::text`, raw).Scan(&f.policyID); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO test_money_recipients(user_id,enabled) VALUES($1,TRUE)`, f.user); err != nil {
		t.Fatal(err)
	}
	f.service = NewService(pool, "staging", fixedReferences{})
	return f
}
func (f *fixture) enable(t *testing.T) {
	t.Helper()
	if _, err := f.pool.Exec(f.ctx, `UPDATE test_money_control SET enabled=TRUE,withdrawal_limits_ready=TRUE,environment='staging',policy_id=$1`, f.policyID); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) input(key string) Input {
	return Input{RecipientID: f.user, AccountID: f.account, AssetID: f.asset, AmountAtomic: "1000000000000000000", Reason: "named tester allocation", IdempotencyKey: key}
}
func (f *fixture) propose(t *testing.T, key string) Request {
	t.Helper()
	r, err := f.service.Propose(f.ctx, f.maker, f.input(key))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func (f *fixture) count(t *testing.T, query string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(f.ctx, query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPostgresDisabledAndEnvironmentBoundary(t *testing.T) {
	f := databaseFixture(t)
	if _, err := f.service.Propose(f.ctx, f.maker, f.input("one")); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	f.enable(t)
	if _, err := NewService(f.pool, "production", fixedReferences{}).Propose(f.ctx, f.maker, f.input("one")); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE test_money_control SET environment='production'"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Propose(f.ctx, f.maker, f.input("one")); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM test_money_requests"); n != 0 {
		t.Fatal(n)
	}
}

func TestPostgresGrantIsBalancedAuditedAndIdempotent(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	r := f.propose(t, "one")
	duplicate := f.propose(t, "one")
	if duplicate.ID != r.ID {
		t.Fatal("duplicate request")
	}
	changed := f.input("one")
	changed.AmountAtomic = "2"
	if _, err := f.service.Propose(f.ctx, f.maker, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	g, err := f.service.Approve(f.ctx, f.checker, r.ID, "approval")
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.service.Approve(f.ctx, f.checker, r.ID, "approval")
	if err != nil || again != g {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if g.ValueUSDTAtomic != "250000000000" {
		t.Fatal(g)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM test_money_grants"); n != 1 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM postings"); n != 2 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM audit_events WHERE action LIKE 'test_money.%'"); n != 2 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM outbox_events WHERE event_type LIKE 'test_money.%'"); n != 2 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM deposits"); n != 0 {
		t.Fatal("issuance fabricated deposit")
	}
	var amount string
	if err = f.pool.QueryRow(f.ctx, `SELECT SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END)::text FROM postings`).Scan(&amount); err != nil || amount != "0" {
		t.Fatalf("balance: %s %v", amount, err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM accounts WHERE name LIKE 'test-money-issuance-%' AND status='frozen' AND user_id IS NULL"); n != 1 {
		t.Fatal("spendable counterpart")
	}
	if _, err = f.pool.Exec(f.ctx, "DELETE FROM test_money_grants"); err == nil {
		t.Fatal("mutable grant")
	}
	if _, err = f.pool.Exec(f.ctx, "UPDATE test_money_requests SET reason='rewritten history'"); err == nil {
		t.Fatal("mutable request")
	}
	if _, err = f.pool.Exec(f.ctx, "DELETE FROM test_money_policies"); err == nil {
		t.Fatal("mutable policy")
	}
	raw, err := os.ReadFile("../../migrations/000068_staging_test_money.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(f.ctx, string(raw)); err == nil {
		t.Fatal("destructive rollback allowed")
	}
}

func TestPostgresRolesEligibilityAndPolicyAreRechecked(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	if _, err := f.service.Propose(f.ctx, f.user, f.input("bad")); !errors.Is(err, ErrRole) {
		t.Fatal(err)
	}
	r := f.propose(t, "one")
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,'treasury_approver')`, f.maker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.maker, r.ID, "same"); !errors.Is(err, ErrChecker) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM user_roles WHERE user_id=$1 AND role='treasury_operator'`, f.maker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "revoked"); !errors.Is(err, ErrRole) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO user_roles(user_id,role) VALUES($1,'treasury_operator')`, f.maker); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE test_money_recipients SET enabled=FALSE"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "ineligible"); !errors.Is(err, ErrRecipient) {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, "UPDATE test_money_recipients SET enabled=TRUE"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `WITH p AS (INSERT INTO test_money_policies(policy) SELECT policy FROM test_money_policies LIMIT 1 RETURNING id) UPDATE test_money_control SET policy_id=(SELECT id FROM p)`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "changed"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM postings"); n != 0 {
		t.Fatal(n)
	}
}

func TestPostgresConcurrentApprovalsCannotExceedTesterCeiling(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	requests := make([]Request, 2)
	for i := range requests {
		in := f.input(fmt.Sprintf("request-%d", i))
		in.AmountAtomic = "2400000000000000000" // 6000 USDT each
		r, err := f.service.Propose(f.ctx, f.maker, in)
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = r
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i, r := range requests {
		wg.Add(1)
		go func(i int, r Request) {
			defer wg.Done()
			<-start
			_, err := f.service.Approve(f.ctx, f.checker, r.ID, fmt.Sprintf("approve-%d", i))
			results <- err
		}(i, r)
	}
	close(start)
	wg.Wait()
	close(results)
	successes, limits := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrLimit) {
			limits++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || limits != 1 {
		t.Fatalf("successes=%d limits=%d", successes, limits)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM test_money_grants"); n != 1 {
		t.Fatal(n)
	}
}

func TestPostgresOutboxFailureRollsBackAllIssuance(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	r := f.propose(t, "one")
	if _, err := f.pool.Exec(f.ctx, `ALTER TABLE outbox_events ADD CONSTRAINT reject_test_issuance CHECK(event_type<>'test_money.issued')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "approval"); err == nil || !strings.Contains(err.Error(), "reject_test_issuance") {
		t.Fatalf("expected injected failure: %v", err)
	}
	for _, table := range []string{"postings", "journals", "test_money_grants"} {
		if n := f.count(t, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Fatalf("%s survived rollback: %d", table, n)
		}
	}
	if n := f.count(t, "SELECT COUNT(*) FROM accounts WHERE name LIKE 'test-money-issuance-%'"); n != 0 {
		t.Fatal(n)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM audit_events WHERE action='test_money.issued'"); n != 0 {
		t.Fatal(n)
	}
	if _, err := f.pool.Exec(f.ctx, "ALTER TABLE outbox_events DROP CONSTRAINT reject_test_issuance"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, r.ID, "approval"); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresApprovalKeyCannotApproveAnotherRequest(t *testing.T) {
	f := databaseFixture(t)
	f.enable(t)
	one := f.propose(t, "one")
	two := f.propose(t, "two")
	if _, err := f.service.Approve(f.ctx, f.checker, one.ID, "approval"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, two.ID, "approval"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := f.service.Approve(f.ctx, f.checker, one.ID, "another"); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if n := f.count(t, "SELECT COUNT(*) FROM test_money_grants"); n != 1 {
		t.Fatal(n)
	}
}
