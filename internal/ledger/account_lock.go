package ledger

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// AccountAssetLocker is implemented by pgx.Tx. Keeping the interface narrow
// makes the lock rule independently testable and prevents balance-changing
// packages from inventing incompatible serialization mechanisms.
type AccountAssetLocker interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// LockAccountAsset serializes every balance-changing operation for one
// account/asset pair until the surrounding PostgreSQL transaction completes.
// All debit paths must acquire these locks in sorted key order when more than
// one balance is involved.
func LockAccountAsset(ctx context.Context, tx AccountAssetLocker, accountID, assetID string) error {
	if tx == nil || accountID == "" || assetID == "" {
		return errors.New("account and asset are required for ledger lock")
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, AccountAssetLockKey(accountID, assetID))
	return err
}

func AccountAssetLockKey(accountID, assetID string) string {
	return accountID + ":" + assetID
}
