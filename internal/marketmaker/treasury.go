package marketmaker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/ledger"
	"github.com/limiance/backend/internal/testmoney"
)

const stagingTreasuryLimitUSDTAtomic = "1000000000000"

type TreasuryInventoryInput struct {
	AssetID, AmountAtomic, Reason, IdempotencyKey string
}

type TreasuryInventoryRequest struct {
	ID, ProposedBy, AssetID, AmountAtomic, Reason string
}

type TreasuryInventoryGrant struct {
	RequestID, AccountID, JournalID, ValueUSDTAtomic string
}

type TreasuryInventoryService struct {
	pool        *pgxpool.Pool
	environment string
	references  testmoney.ReferenceSource
}

func NewTreasuryInventoryService(pool *pgxpool.Pool, environment string, references testmoney.ReferenceSource) *TreasuryInventoryService {
	return &TreasuryInventoryService{pool: pool, environment: environment, references: references}
}

func (s *TreasuryInventoryService) Propose(ctx context.Context, actor string, in TreasuryInventoryInput) (TreasuryInventoryRequest, error) {
	if s == nil || s.pool == nil || s.environment != "staging" {
		return TreasuryInventoryRequest{}, ErrActivationState
	}
	if actor == "" || in.AssetID == "" || !positiveAtomic(in.AmountAtomic) || len(strings.TrimSpace(in.Reason)) < 8 || len(in.Reason) > 1000 || in.IdempotencyKey == "" || strings.TrimSpace(in.IdempotencyKey) != in.IdempotencyKey || len(in.IdempotencyKey) > 255 {
		return TreasuryInventoryRequest{}, ErrActivationInput
	}
	payload, _ := json.Marshal(in)
	hash := sha256.Sum256(payload)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TreasuryInventoryRequest{}, err
	}
	defer tx.Rollback(ctx)
	if err = requireTreasuryRole(ctx, tx, actor, "treasury_operator"); err != nil {
		return TreasuryInventoryRequest{}, err
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND network='internal_spot' AND status='enabled')`, in.AssetID).Scan(&valid); err != nil || !valid {
		return TreasuryInventoryRequest{}, ErrActivationState
	}
	var out TreasuryInventoryRequest
	var same bool
	err = tx.QueryRow(ctx, `INSERT INTO staging_market_maker_treasury_requests(proposed_by,asset_id,amount_atomic,reason,idempotency_key,request_hash) VALUES($1,$2,$3::numeric,$4,$5,$6) ON CONFLICT(proposed_by,idempotency_key) DO NOTHING RETURNING id::text,proposed_by::text,asset_id::text,amount_atomic::text,reason,true`, actor, in.AssetID, in.AmountAtomic, strings.TrimSpace(in.Reason), in.IdempotencyKey, hash[:]).Scan(&out.ID, &out.ProposedBy, &out.AssetID, &out.AmountAtomic, &out.Reason, &same)
	if err == pgx.ErrNoRows {
		err = tx.QueryRow(ctx, `SELECT id::text,proposed_by::text,asset_id::text,amount_atomic::text,reason,request_hash=$3 FROM staging_market_maker_treasury_requests WHERE proposed_by=$1 AND idempotency_key=$2`, actor, in.IdempotencyKey, hash[:]).Scan(&out.ID, &out.ProposedBy, &out.AssetID, &out.AmountAtomic, &out.Reason, &same)
	}
	if err != nil {
		return out, err
	}
	if !same {
		return out, ErrIdempotencyConflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason) SELECT $1,'admin','market_maker.treasury_proposed','market_maker_treasury',$2,$3 WHERE NOT EXISTS(SELECT 1 FROM audit_events WHERE action='market_maker.treasury_proposed' AND resource_id=$2)`, actor, out.ID, out.Reason); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) SELECT 'market_maker.treasury_proposed','market_maker_treasury',$1,jsonb_build_object('request_id',$1::text) WHERE NOT EXISTS(SELECT 1 FROM outbox_events WHERE event_type='market_maker.treasury_proposed' AND aggregate_id=$1)`, out.ID); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *TreasuryInventoryService) Approve(ctx context.Context, actor, requestID, key string) (TreasuryInventoryGrant, error) {
	if s == nil || s.pool == nil || s.environment != "staging" || actor == "" || requestID == "" || key == "" || strings.TrimSpace(key) != key || len(key) > 255 {
		return TreasuryInventoryGrant{}, ErrActivationInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TreasuryInventoryGrant{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('staging-market-maker-treasury',0))`); err != nil {
		return TreasuryInventoryGrant{}, err
	}
	if err = requireTreasuryRole(ctx, tx, actor, "treasury_approver"); err != nil {
		return TreasuryInventoryGrant{}, err
	}
	var req TreasuryInventoryRequest
	var symbol string
	var decimals int
	if err = tx.QueryRow(ctx, `SELECT r.id::text,r.proposed_by::text,r.asset_id::text,r.amount_atomic::text,r.reason,a.symbol,a.decimals FROM staging_market_maker_treasury_requests r JOIN assets a ON a.id=r.asset_id AND a.network='internal_spot' AND a.status='enabled' WHERE r.id=$1 FOR SHARE OF r,a`, requestID).Scan(&req.ID, &req.ProposedBy, &req.AssetID, &req.AmountAtomic, &req.Reason, &symbol, &decimals); err != nil {
		return TreasuryInventoryGrant{}, ErrActivationState
	}
	if actor == req.ProposedBy {
		return TreasuryInventoryGrant{}, ErrSameChecker
	}
	var old TreasuryInventoryGrant
	err = tx.QueryRow(ctx, `SELECT g.request_id::text,a.id::text,g.journal_id::text,g.value_usdt_atomic::text FROM staging_market_maker_treasury_grants g JOIN journals j ON j.id=g.journal_id JOIN postings p ON p.journal_id=j.id AND p.direction='credit' JOIN accounts a ON a.id=p.account_id AND a.kind='system' AND a.name='staging-market-maker-treasury' WHERE g.approved_by=$1 AND g.approval_key=$2`, actor, key).Scan(&old.RequestID, &old.AccountID, &old.JournalID, &old.ValueUSDTAtomic)
	if err == nil {
		if old.RequestID != requestID {
			return old, ErrIdempotencyConflict
		}
		return old, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return old, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM staging_market_maker_treasury_grants WHERE request_id=$1)`, requestID).Scan(&exists); err != nil || exists {
		return old, ErrActivationState
	}
	if err = requireTreasuryRole(ctx, tx, req.ProposedBy, "treasury_operator"); err != nil {
		return old, err
	}
	value, evidence, err := s.value(ctx, tx, req, symbol, decimals)
	if err != nil {
		return old, err
	}
	var used string
	if err = tx.QueryRow(ctx, `SELECT COALESCE(sum(value_usdt_atomic),0)::text FROM staging_market_maker_treasury_grants`).Scan(&used); err != nil {
		return old, err
	}
	u, _ := new(big.Int).SetString(used, 10)
	v, _ := new(big.Int).SetString(value, 10)
	limit, _ := new(big.Int).SetString(stagingTreasuryLimitUSDTAtomic, 10)
	if new(big.Int).Add(u, v).Cmp(limit) > 0 {
		return old, ErrActivationState
	}
	var treasuryID, counterpartID string
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(user_id,kind,name,status) VALUES(NULL,'system','staging-market-maker-treasury','active') ON CONFLICT(name) WHERE kind='system' DO NOTHING`); err != nil {
		return old, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE user_id IS NULL AND kind='system' AND name='staging-market-maker-treasury' AND status='active' FOR SHARE`).Scan(&treasuryID); err != nil {
		return old, ErrActivationState
	}
	if _, err = tx.Exec(ctx, `INSERT INTO accounts(user_id,kind,name,status) VALUES(NULL,'system',$1,'frozen') ON CONFLICT(name) WHERE kind='system' DO NOTHING`, "staging-market-maker-issuance-"+req.AssetID); err != nil {
		return old, err
	}
	if err = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE user_id IS NULL AND kind='system' AND name=$1 AND status='frozen' FOR SHARE`, "staging-market-maker-issuance-"+req.AssetID).Scan(&counterpartID); err != nil {
		return old, ErrActivationState
	}
	ids := []string{treasuryID, counterpartID}
	sort.Strings(ids)
	for _, id := range ids {
		if err = ledger.LockAccountAsset(ctx, tx, id, req.AssetID); err != nil {
			return old, err
		}
	}
	old = newTreasuryGrant(req.ID, treasuryID, value)
	if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'market_maker_treasury_issuance',$2) RETURNING id::text`, "market-maker-treasury:"+req.ID, req.ID).Scan(&old.JournalID); err != nil {
		return old, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES($1,$2,$3,'available','debit',$4::numeric),($1,$5,$3,'available','credit',$4::numeric)`, old.JournalID, counterpartID, req.AssetID, req.AmountAtomic, treasuryID); err != nil {
		return old, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO staging_market_maker_treasury_grants(request_id,approved_by,approval_key,journal_id,value_usdt_atomic,evidence) VALUES($1,$2,$3,$4,$5::numeric,$6)`, req.ID, actor, key, old.JournalID, value, evidence); err != nil {
		return old, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'admin','market_maker.treasury_issued','market_maker_treasury',$2,$3,jsonb_build_object('journal_id',$4::text,'value_usdt_atomic',$5::text))`, actor, req.ID, req.Reason, old.JournalID, value); err != nil {
		return old, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('market_maker.treasury_issued','market_maker_treasury',$1,jsonb_build_object('request_id',$1::text,'journal_id',$2::text))`, req.ID, old.JournalID); err != nil {
		return old, err
	}
	return old, tx.Commit(ctx)
}

func newTreasuryGrant(request, account, value string) TreasuryInventoryGrant {
	return TreasuryInventoryGrant{RequestID: request, AccountID: account, ValueUSDTAtomic: value}
}

func requireTreasuryRole(ctx context.Context, tx pgx.Tx, actor, role string) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=$1 AND u.status='active' AND r.role=$2)`, actor, role).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return ErrActivationRole
	}
	return nil
}

func (s *TreasuryInventoryService) value(ctx context.Context, tx pgx.Tx, req TreasuryInventoryRequest, symbol string, decimals int) (string, []byte, error) {
	if strings.EqualFold(symbol, "USDT") {
		if decimals < 0 || decimals > 8 {
			return "", nil, ErrActivationState
		}
		n, _ := new(big.Int).SetString(req.AmountAtomic, 10)
		n.Mul(n, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(8-decimals)), nil))
		evidence, _ := json.Marshal(map[string]string{"basis": "USDT quote unit"})
		return n.String(), evidence, nil
	}
	if s.references == nil {
		return "", nil, testmoney.ErrReference
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT p.policy FROM test_money_control c JOIN test_money_policies p ON p.id=c.policy_id WHERE c.singleton AND c.enabled AND c.environment='staging'`).Scan(&raw); err != nil {
		return "", nil, err
	}
	var policy testmoney.Policy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return "", nil, err
	}
	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	obs, err := s.references.Observations(qctx, req.AssetID)
	cancel()
	if err != nil {
		return "", nil, err
	}
	value, err := policy.Value(req.AssetID, req.AmountAtomic, obs, time.Now().UTC())
	evidence, _ := json.Marshal(obs)
	return value, evidence, err
}
