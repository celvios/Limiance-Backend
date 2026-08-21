package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/platform/database"
)

type migration struct {
	name string
	sql  []byte
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, config.Load().DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	if err := apply(ctx, pool); err != nil {
		fatal(err)
	}
	fmt.Println("migrations applied")
}

func apply(ctx context.Context, db interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}) error {
	if err := ensureTable(ctx, db); err != nil {
		return err
	}
	migrations, err := load("migrations")
	if err != nil {
		return err
	}
	for _, item := range migrations {
		if err := applyOne(ctx, db, item); err != nil {
			return fmt.Errorf("apply %s: %w", item.name, err)
		}
	}
	return nil
}

func ensureTable(ctx context.Context, db interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func applyOne(ctx context.Context, db interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, item migration) error {
	tx, err := db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	checksum := fmt.Sprintf("%x", sha256.Sum256(item.sql))
	var existing string
	err = tx.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE name = $1`, item.name).Scan(&existing)
	if err == nil {
		if existing != checksum {
			return fmt.Errorf("migration checksum changed after application")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err = tx.Exec(ctx, string(item.sql)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`, item.name, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func load(dir string) ([]migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	items := make([]migration, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		items = append(items, migration{name: entry.Name(), sql: body})
	}
	if len(items) == 0 {
		return nil, fs.ErrNotExist
	}
	sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
	return items, nil
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
