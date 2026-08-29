package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type recordingLocker struct {
	query string
	args  []any
	err   error
}

func (locker *recordingLocker) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	locker.query = query
	locker.args = args
	return pgconn.CommandTag{}, locker.err
}

func TestLockAccountAssetUsesCanonicalTransactionLock(t *testing.T) {
	locker := &recordingLocker{}
	if err := LockAccountAsset(context.Background(), locker, "account-1", "asset-1"); err != nil {
		t.Fatalf("lock account asset: %v", err)
	}
	if locker.query != `SELECT pg_advisory_xact_lock(hashtextextended($1,0))` {
		t.Fatalf("unexpected lock query %q", locker.query)
	}
	if len(locker.args) != 1 || locker.args[0] != "account-1:asset-1" {
		t.Fatalf("unexpected lock key %#v", locker.args)
	}
}

func TestLockAccountAssetRejectsIncompleteIdentity(t *testing.T) {
	if err := LockAccountAsset(context.Background(), &recordingLocker{}, "", "asset-1"); err == nil {
		t.Fatal("expected incomplete identity to fail")
	}
}

func TestLockAccountAssetPropagatesDatabaseFailure(t *testing.T) {
	want := errors.New("database unavailable")
	err := LockAccountAsset(context.Background(), &recordingLocker{err: want}, "account-1", "asset-1")
	if !errors.Is(err, want) {
		t.Fatalf("expected database failure, got %v", err)
	}
}
