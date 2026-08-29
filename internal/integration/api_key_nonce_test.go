package integration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/datamanager"
)

func TestAPIKeyNonceIsConsumedExactlyOnce(t *testing.T) {
	databaseURL := os.Getenv("LIMIANCE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set LIMIANCE_TEST_DATABASE_URL to run API nonce tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	fixture := fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	var userID, accountID string
	if err = pool.QueryRow(ctx, `INSERT INTO users(email,password_hash,country_code,status) VALUES($1,'test-only','NG','active') RETURNING id::text`, fixture+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `INSERT INTO accounts(user_id,kind,name) VALUES($1,'funding',$2) RETURNING id::text`, userID, fixture).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	keyHash := sha256.Sum256([]byte(fixture + "-key"))
	if _, err = pool.Exec(ctx, `INSERT INTO api_keys(user_id,account_id,name,key_hash,secret_ciphertext,scope) VALUES($1,$2,$3,$4,'test','trade')`, userID, accountID, fixture, keyHash[:]); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM api_key_nonces WHERE key_hash=$1`, keyHash[:])
		_, _ = pool.Exec(context.Background(), `DELETE FROM api_keys WHERE key_hash=$1`, keyHash[:])
		_, _ = pool.Exec(context.Background(), `DELETE FROM accounts WHERE id=$1`, accountID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
	}()
	nonceHash := sha256.Sum256([]byte("same-nonce"))
	manager := datamanager.New(pool)
	results := make(chan bool, 8)
	errorsFound := make(chan error, 8)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			consumed, consumeErr := manager.ConsumeAPIKeyNonce(ctx, keyHash[:], nonceHash[:], time.Now().Add(time.Minute))
			if consumeErr != nil {
				errorsFound <- consumeErr
				return
			}
			results <- consumed
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsFound)
	for consumeErr := range errorsFound {
		t.Fatal(consumeErr)
	}
	accepted := 0
	for consumed := range results {
		if consumed {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("nonce accepted %d times, want 1", accepted)
	}
}
