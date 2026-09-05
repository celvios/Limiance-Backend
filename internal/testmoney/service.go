package testmoney

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ReferenceSource interface {
	Observations(context.Context, string) ([]Observation, error)
}

type Input struct {
	RecipientID    string
	AccountID      string
	AssetID        string
	AmountAtomic   string
	Reason         string
	IdempotencyKey string
}

type Request struct {
	ID         string
	ProposedBy string
	PolicyID   string
	Input      Input
}

type Grant struct {
	RequestID       string
	JournalID       string
	ValueUSDTAtomic string
}

type Service struct {
	pool        *pgxpool.Pool
	environment string
	references  ReferenceSource
}

// NewService does not enable issuance. The default database control is disabled;
// no API route or deployment configuration is wired to this service.
func NewService(pool *pgxpool.Pool, environment string, references ReferenceSource) *Service {
	return &Service{pool: pool, environment: environment, references: references}
}

func (s *Service) begin(ctx context.Context) (pgx.Tx, error) {
	if s.environment != "staging" || s.pool == nil {
		return nil, ErrDisabled
	}
	return s.pool.BeginTx(ctx, pgx.TxOptions{})
}

func (s *Service) Propose(ctx context.Context, actor string, in Input) (Request, error) {
	if actor == "" || in.RecipientID == "" || in.AccountID == "" || in.AssetID == "" || len(strings.TrimSpace(in.Reason)) < 8 || len(in.Reason) > 1000 || !validKey(in.IdempotencyKey) {
		return Request{}, ErrInput
	}
	if _, err := atomic(in.AmountAtomic, false); err != nil {
		return Request{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Request{}, err
	}
	defer tx.Rollback(ctx)
	policyID, policy, err := loadPolicy(ctx, tx)
	if err != nil {
		return Request{}, err
	}
	if err = role(ctx, tx, actor, "treasury_operator"); err != nil {
		return Request{}, err
	}
	payload, _ := json.Marshal(in)
	hash := sha256.Sum256(payload)
	var old Request
	var oldHash []byte
	err = tx.QueryRow(ctx, `SELECT id::text,request_hash FROM test_money_requests WHERE proposed_by=$1 AND idempotency_key=$2`, actor, in.IdempotencyKey).Scan(&old.ID, &oldHash)
	if err == nil {
		if string(oldHash) != string(hash[:]) {
			return Request{}, ErrConflict
		}
		old, err = loadRequest(ctx, tx, old.ID)
		if err != nil {
			return Request{}, err
		}
		return old, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return Request{}, err
	}
	if err = eligible(ctx, tx, policy, in); err != nil {
		return Request{}, err
	}
	r := Request{ProposedBy: actor, PolicyID: policyID, Input: in}
	err = tx.QueryRow(ctx, `INSERT INTO test_money_requests(proposed_by,recipient_id,account_id,asset_id,amount_atomic,reason,policy_id,idempotency_key,request_hash)
 VALUES($1,$2,$3,$4,$5::numeric,$6,$7,$8,$9) RETURNING id::text`, actor, in.RecipientID, in.AccountID, in.AssetID, in.AmountAtomic, in.Reason, policyID, in.IdempotencyKey, hash[:]).Scan(&r.ID)
	if err != nil {
		return Request{}, err
	}
	if err = record(ctx, tx, actor, "test_money.proposed", r.ID, in.Reason, r); err != nil {
		return Request{}, err
	}
	return r, tx.Commit(ctx)
}

func validKey(key string) bool {
	return len(key) > 0 && len(key) <= 255 && strings.TrimSpace(key) == key
}

func (s *Service) Approve(ctx context.Context, actor, requestID, key string) (Grant, error) {
	if actor == "" || requestID == "" || !validKey(key) {
		return Grant{}, ErrInput
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx)
	policyID, policy, err := loadPolicy(ctx, tx)
	if err != nil {
		return Grant{}, err
	}
	if err = role(ctx, tx, actor, "treasury_approver"); err != nil {
		return Grant{}, err
	}
	request, err := loadRequest(ctx, tx, requestID)
	if err != nil {
		return Grant{}, err
	}
	if actor == request.ProposedBy {
		return Grant{}, ErrChecker
	}
	var grant Grant
	err = tx.QueryRow(ctx, `SELECT request_id::text,journal_id::text,value_usdt_atomic::text FROM test_money_grants WHERE approved_by=$1 AND approval_key=$2`, actor, key).Scan(&grant.RequestID, &grant.JournalID, &grant.ValueUSDTAtomic)
	if err == nil {
		if grant.RequestID != requestID {
			return Grant{}, ErrConflict
		}
		return grant, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return Grant{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM test_money_grants WHERE request_id=$1)`, requestID).Scan(&exists); err != nil {
		return Grant{}, err
	}
	if exists {
		return Grant{}, ErrConflict
	}
	if request.PolicyID != policyID {
		return Grant{}, ErrConflict
	}
	if err = role(ctx, tx, request.ProposedBy, "treasury_operator"); err != nil {
		return Grant{}, err
	}
	if err = eligible(ctx, tx, policy, request.Input); err != nil {
		return Grant{}, err
	}
	if s.references == nil {
		return Grant{}, ErrReference
	}
	// This bounded lookup is exclusively on the administrative path.
	quoteCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	observations, err := s.references.Observations(quoteCtx, request.Input.AssetID)
	cancel()
	if err != nil {
		return Grant{}, err
	}
	value, err := policy.Value(request.Input.AssetID, request.Input.AmountAtomic, observations, time.Now().UTC())
	if err != nil {
		return Grant{}, err
	}
	var userUsed, globalUsed string
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(g.value_usdt_atomic) FILTER(WHERE r.recipient_id=$1),0)::text,COALESCE(SUM(g.value_usdt_atomic),0)::text
 FROM test_money_grants g JOIN test_money_requests r ON r.id=g.request_id`, request.Input.RecipientID).Scan(&userUsed, &globalUsed)
	if err != nil {
		return Grant{}, err
	}
	if err = policy.CheckQuota(value, userUsed, globalUsed); err != nil {
		return Grant{}, err
	}
	grant, err = postGrant(ctx, tx, request, actor, key, value, observations)
	if err != nil {
		return Grant{}, err
	}
	return grant, tx.Commit(ctx)
}
