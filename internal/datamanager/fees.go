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
		INSERT INTO user_fee_tier (user_id,tier_level,updated_at)
		SELECT u.id,COALESCE((SELECT MAX(t.level) FROM vip_tiers t WHERE t.min_30d_volume <= COALESCE(v.volume_usd,0)),0),NOW()
		FROM users u LEFT JOIN user_volume_30d v ON v.user_id=u.id
		ON CONFLICT (user_id) DO UPDATE SET tier_level=EXCLUDED.tier_level,updated_at=NOW()`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
