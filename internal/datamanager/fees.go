package datamanager

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type UserFees struct {
	TierLevel   int    `json:"tier_level"`
	TierName    string `json:"tier_name"`
	MakerFeeBPS int    `json:"maker_fee_bps"`
	TakerFeeBPS int    `json:"taker_fee_bps"`
	VolumeUSD   string `json:"volume_usd"`
}

type FeePolicyInput struct {
	ActorID       string
	TierLevel     int
	MakerFeeBPS   int
	TakerFeeBPS   int
	MinimumVolume string
	Reason        string
}

func (m *Manager) UserFees(ctx context.Context, userID string) (UserFees, error) {
	var fees UserFees
	err := m.pool.QueryRow(ctx, `
		SELECT t.level,t.name,t.maker_fee_bps,t.taker_fee_bps,COALESCE(v.volume_usd,0)
		FROM users u
		LEFT JOIN user_fee_tier ft ON ft.user_id=u.id
		JOIN vip_tiers t ON t.level=COALESCE(ft.tier_level,0)
		LEFT JOIN user_volume_30d v ON v.user_id=u.id
		WHERE u.id=$1`, userID).Scan(&fees.TierLevel, &fees.TierName, &fees.MakerFeeBPS, &fees.TakerFeeBPS, &fees.VolumeUSD)
	return fees, err
}

func (m *Manager) RefreshUserFeeTiers(ctx context.Context) error {
	windowStart := time.Now().UTC().Add(-30 * 24 * time.Hour)
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_volume_30d (user_id,volume_usd,window_start,updated_at)
		SELECT u.id,COALESCE(SUM(v.volume),0),$1,NOW()
		FROM users u
		LEFT JOIN (
			SELECT maker_user_id AS user_id,quantity*price AS volume
			FROM trades WHERE traded_at >= $1
			UNION ALL
			SELECT taker_user_id AS user_id,quantity*price AS volume
			FROM trades WHERE traded_at >= $1
		) v ON v.user_id=u.id
		GROUP BY u.id
		ON CONFLICT (user_id) DO UPDATE SET volume_usd=EXCLUDED.volume_usd,window_start=EXCLUDED.window_start,updated_at=NOW()`, windowStart); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		WITH resolved AS (
			SELECT u.id AS user_id,COALESCE(ft.tier_level,0) AS previous_tier,
			       COALESCE((SELECT MAX(t.level) FROM vip_tiers t WHERE t.min_30d_volume <= COALESCE(v.volume_usd,0)),0) AS new_tier,
			       COALESCE(v.volume_usd,0) AS volume
			FROM users u
			LEFT JOIN user_fee_tier ft ON ft.user_id=u.id
			LEFT JOIN user_volume_30d v ON v.user_id=u.id
		)
		INSERT INTO fee_tier_audit_history(user_id,previous_tier_level,new_tier_level,rolling_volume_usd)
		SELECT user_id,previous_tier,new_tier,volume FROM resolved WHERE previous_tier<>new_tier`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_fee_tier (user_id,tier_level,updated_at)
		SELECT u.id,COALESCE((SELECT MAX(t.level) FROM vip_tiers t WHERE t.min_30d_volume <= COALESCE(v.volume_usd,0)),0),NOW()
		FROM users u LEFT JOIN user_volume_30d v ON v.user_id=u.id
		ON CONFLICT (user_id) DO UPDATE SET tier_level=EXCLUDED.tier_level,updated_at=NOW()`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (m *Manager) UpdateFeePolicy(ctx context.Context, input FeePolicyInput) error {
	if input.TierLevel < 0 || input.MakerFeeBPS < -10000 || input.MakerFeeBPS > 10000 || input.TakerFeeBPS < 0 || input.TakerFeeBPS > 10000 || input.MinimumVolume == "" || input.Reason == "" {
		return pgx.ErrNoRows
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `UPDATE vip_tiers SET maker_fee_bps=$2,taker_fee_bps=$3,min_30d_volume=$4::numeric WHERE level=$1`, input.TierLevel, input.MakerFeeBPS, input.TakerFeeBPS, input.MinimumVolume)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	var actor any
	if input.ActorID != "" {
		actor = input.ActorID
	}
	if _, err = tx.Exec(ctx, `INSERT INTO fee_policy_audit_history(tier_level,actor_id,maker_fee_bps,taker_fee_bps,min_30d_volume,reason) VALUES($1,$2,$3,$4,$5::numeric,$6)`, input.TierLevel, actor, input.MakerFeeBPS, input.TakerFeeBPS, input.MinimumVolume, input.Reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
