package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/datamanager"
)

func TestFeeEngineRollingVolumeBoundariesAndAudit(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run PostgreSQL fee engine tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer pool.Close()
	fixture := fmt.Sprintf("fee-%d", time.Now().UnixNano())
	var userID, counterpartyID, subaccountID string
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'test-only','NG','active') RETURNING id::text`, fixture+"@example.test").Scan(&userID); err != nil {
		t.Fatalf("create fee user: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'test-only','NG','active') RETURNING id::text`, fixture+"-counterparty@example.test").Scan(&counterpartyID); err != nil {
		t.Fatalf("create fee counterparty: %v", err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'subaccount',$2) RETURNING id::text`, userID, fixture+"-strategy").Scan(&subaccountID); err != nil {
		t.Fatalf("create fee subaccount: %v", err)
	}
	const policyTier = 90
	if _, err = pool.Exec(ctx, `INSERT INTO vip_tiers(level,name,maker_fee_bps,taker_fee_bps,min_30d_volume) VALUES($1,$2,0,1,9999999999999999)`, policyTier, fixture); err != nil {
		t.Fatalf("create isolated policy tier: %v", err)
	}
	defer cleanupFeeEngineFixture(pool, fixture, userID, counterpartyID, policyTier)

	now := time.Now().UTC()
	if _, err = pool.Exec(ctx, `INSERT INTO trades(pair,maker_user_id,taker_user_id,quantity,price,traded_at) VALUES
		($1,$2,$3,1,50000,$4),($1,$2,$3,10,50000,$5)`, fixture, userID, counterpartyID, now.Add(-time.Hour), now.Add(-31*24*time.Hour)); err != nil {
		t.Fatalf("insert rolling-volume trades: %v", err)
	}
	manager := datamanager.New(pool)
	rows, err := pool.Query(ctx, `SELECT level,maker_fee_bps,taker_fee_bps FROM vip_tiers WHERE level IN (0,3,5) ORDER BY level`)
	if err != nil {
		t.Fatalf("read default fee tiers: %v", err)
	}
	wantTiers := [][3]int{{0, 10, 10}, {3, 4, 8}, {5, 0, 6}}
	index := 0
	for rows.Next() {
		var got [3]int
		if err = rows.Scan(&got[0], &got[1], &got[2]); err != nil {
			rows.Close()
			t.Fatalf("scan default fee tier: %v", err)
		}
		if index >= len(wantTiers) || got != wantTiers[index] {
			rows.Close()
			t.Fatalf("unexpected default fee tier: got=%v index=%d", got, index)
		}
		index++
	}
	rows.Close()
	if index != len(wantTiers) {
		t.Fatalf("expected %d default fee tiers, got %d", len(wantTiers), index)
	}
	if err = manager.RefreshUserFeeTiers(ctx); err != nil {
		t.Fatalf("refresh fee tiers: %v", err)
	}
	resolved, err := manager.UserFees(ctx, userID)
	if err != nil {
		t.Fatalf("resolve upgraded tier: %v", err)
	}
	if resolved.TierLevel != 1 || resolved.MakerFeeBPS != 8 || resolved.TakerFeeBPS != 10 || resolved.VolumeUSD != "50000.00000000" {
		t.Fatalf("unexpected threshold result: %+v", resolved)
	}
	var upgrades int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM fee_tier_audit_history WHERE user_id=$1 AND previous_tier_level=0 AND new_tier_level=1`, userID).Scan(&upgrades); err != nil || upgrades != 1 {
		t.Fatalf("tier upgrade audit missing: count=%d err=%v", upgrades, err)
	}

	if _, err = pool.Exec(ctx, `UPDATE trades SET traded_at=$2 WHERE pair=$1 AND traded_at>$2`, fixture, now.Add(-31*24*time.Hour)); err != nil {
		t.Fatalf("roll volume outside window: %v", err)
	}
	if err = manager.RefreshUserFeeTiers(ctx); err != nil {
		t.Fatalf("refresh rolled-off tier: %v", err)
	}
	resolved, err = manager.UserFees(ctx, userID)
	if err != nil || resolved.TierLevel != 0 || resolved.VolumeUSD != "0" {
		t.Fatalf("volume did not roll off: fees=%+v err=%v", resolved, err)
	}
	var downgrades int
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM fee_tier_audit_history WHERE user_id=$1 AND previous_tier_level=1 AND new_tier_level=0`, userID).Scan(&downgrades); err != nil || downgrades != 1 {
		t.Fatalf("tier downgrade audit missing: count=%d err=%v", downgrades, err)
	}

	if err = manager.UpdateFeePolicy(ctx, datamanager.FeePolicyInput{ActorID: userID, TierLevel: policyTier, MakerFeeBPS: -2, TakerFeeBPS: 5, MinimumVolume: "9999999999999999.00000000", Reason: "test maker rebate"}); err != nil {
		t.Fatalf("configure maker rebate: %v", err)
	}
	var makerBPS, takerBPS, policyAudits int
	if err = pool.QueryRow(ctx, `SELECT maker_fee_bps,taker_fee_bps FROM vip_tiers WHERE level=$1`, policyTier).Scan(&makerBPS, &takerBPS); err != nil {
		t.Fatalf("read configured policy: %v", err)
	}
	if err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM fee_policy_audit_history WHERE tier_level=$1 AND actor_id=$2 AND maker_fee_bps=-2`, policyTier, userID).Scan(&policyAudits); err != nil {
		t.Fatalf("read policy audit: %v", err)
	}
	if makerBPS != -2 || takerBPS != 5 || policyAudits != 1 {
		t.Fatalf("rebate policy was not audited: maker=%d taker=%d audits=%d", makerBPS, takerBPS, policyAudits)
	}
}

func cleanupFeeEngineFixture(pool *pgxpool.Pool, fixture, userID, counterpartyID string, policyTier int) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM fee_policy_audit_history WHERE tier_level=$1`, policyTier)
	_, _ = pool.Exec(ctx, `DELETE FROM fee_tier_audit_history WHERE user_id=ANY($1::uuid[])`, []string{userID, counterpartyID})
	_, _ = pool.Exec(ctx, `DELETE FROM user_fee_tier WHERE user_id=ANY($1::uuid[])`, []string{userID, counterpartyID})
	_, _ = pool.Exec(ctx, `DELETE FROM user_volume_30d WHERE user_id=ANY($1::uuid[])`, []string{userID, counterpartyID})
	_, _ = pool.Exec(ctx, `DELETE FROM trades WHERE pair=$1`, fixture)
	_, _ = pool.Exec(ctx, `DELETE FROM accounts WHERE user_id=$1`, userID)
	_, _ = pool.Exec(ctx, `DELETE FROM vip_tiers WHERE level=$1`, policyTier)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::uuid[])`, []string{userID, counterpartyID})
}
