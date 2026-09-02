package marketmaker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrActivationInput     = errors.New("invalid market maker activation request")
	ErrActivationRole      = errors.New("market maker activation role required")
	ErrSameChecker         = errors.New("market maker proposer and approver must differ")
	ErrActivationState     = errors.New("market maker activation precondition failed")
	ErrIdempotencyConflict = errors.New("market maker activation idempotency conflict")
)

type PairPolicy struct {
	Pair                   string `json:"pair"`
	SpreadBPS              int64  `json:"spread_bps"`
	QuantityAtomic         string `json:"quantity_atomic"`
	MaxBaseInventoryAtomic string `json:"max_base_inventory_atomic"`
	MaxQuoteNotionalAtomic string `json:"max_quote_notional_atomic"`
	MaxDailyLossAtomic     string `json:"max_daily_loss_atomic"`
	MaxDivergenceBPS       int64  `json:"max_divergence_bps"`
	StaleAfterSeconds      int64  `json:"stale_after_seconds"`
}

type InventoryGrant struct {
	Asset           string `json:"asset"`
	SourceAccountID string `json:"source_account_id"`
	AmountAtomic    string `json:"amount_atomic"`
}

type ActivationInput struct {
	Action               string           `json:"action"`
	MarketMakerEmail     string           `json:"market_maker_email,omitempty"`
	Inventory            []InventoryGrant `json:"inventory,omitempty"`
	Pairs                []PairPolicy     `json:"pairs,omitempty"`
	ReferenceEvidence    bool             `json:"reference_evidence,omitempty"`
	EmergencyStopTested  bool             `json:"emergency_stop_tested,omitempty"`
	LedgerReconciled     bool             `json:"ledger_reconciled,omitempty"`
	OrderLifecycleTested bool             `json:"order_lifecycle_tested,omitempty"`
	Reason               string           `json:"reason"`
	IdempotencyKey       string           `json:"-"`
}

type ActivationRequest struct {
	ID         string    `json:"id"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	ProposedBy string    `json:"proposed_by"`
	ApprovedBy string    `json:"approved_by,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ActivationService struct{ pool *pgxpool.Pool }

func NewActivationService(pool *pgxpool.Pool) *ActivationService {
	return &ActivationService{pool: pool}
}

func validateActivation(input ActivationInput) error {
	input.Action = strings.TrimSpace(input.Action)
	if len(strings.TrimSpace(input.Reason)) < 8 || len(input.Reason) > 1000 || len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 255 {
		return ErrActivationInput
	}
	if input.Action == "release_live" {
		if !input.ReferenceEvidence || !input.EmergencyStopTested || !input.LedgerReconciled || !input.OrderLifecycleTested || input.MarketMakerEmail != "" || len(input.Inventory) != 0 || len(input.Pairs) != 0 {
			return ErrActivationInput
		}
		return nil
	}
	if input.Action != "configure_dry_run" || strings.TrimSpace(input.MarketMakerEmail) == "" || len(input.Inventory) == 0 || len(input.Pairs) == 0 {
		return ErrActivationInput
	}
	seenPairs, seenAssets := map[string]bool{}, map[string]bool{}
	for _, item := range input.Inventory {
		amount, ok := new(big.Int).SetString(item.AmountAtomic, 10)
		asset := strings.ToUpper(strings.TrimSpace(item.Asset))
		if !ok || amount.Sign() <= 0 || asset == "" || item.SourceAccountID == "" || seenAssets[asset] {
			return ErrActivationInput
		}
		seenAssets[asset] = true
	}
	for _, pair := range input.Pairs {
		name := strings.ToUpper(strings.TrimSpace(pair.Pair))
		if name == "" || seenPairs[name] || !positiveAtomic(pair.QuantityAtomic) || !positiveAtomic(pair.MaxBaseInventoryAtomic) || !positiveAtomic(pair.MaxQuoteNotionalAtomic) || !positiveAtomic(pair.MaxDailyLossAtomic) || pair.SpreadBPS < 2 || pair.SpreadBPS > 10000 || pair.MaxDivergenceBPS < 1 || pair.MaxDivergenceBPS > 10000 || pair.StaleAfterSeconds < 1 || pair.StaleAfterSeconds > 300 {
			return ErrActivationInput
		}
		seenPairs[name] = true
	}
	return nil
}

func positiveAtomic(value string) bool {
	n, ok := new(big.Int).SetString(value, 10)
	return ok && n.Sign() > 0
}

func (s *ActivationService) Propose(ctx context.Context, actorID string, input ActivationInput) (ActivationRequest, error) {
	if err := validateActivation(input); err != nil {
		return ActivationRequest{}, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ActivationRequest{}, err
	}
	hash := sha256.Sum256(append([]byte(input.Action+"\x00"), payload...))
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ActivationRequest{}, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=$1 AND u.status='active' AND r.role='treasury_operator')`, actorID).Scan(&allowed); err != nil {
		return ActivationRequest{}, err
	}
	if !allowed {
		return ActivationRequest{}, ErrActivationRole
	}
	var request ActivationRequest
	var created bool
	err = tx.QueryRow(ctx, `INSERT INTO market_maker_activation_requests(action,payload,reason,proposed_by,idempotency_key,request_hash) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (proposed_by,idempotency_key) DO UPDATE SET idempotency_key=EXCLUDED.idempotency_key
		RETURNING id::text,action,status,proposed_by::text,created_at,request_hash=$6,(xmax=0)`, input.Action, payload, strings.TrimSpace(input.Reason), actorID, input.IdempotencyKey, hash[:]).Scan(&request.ID, &request.Action, &request.Status, &request.ProposedBy, &request.CreatedAt, &allowed, &created)
	if err != nil {
		return ActivationRequest{}, err
	}
	if !allowed {
		return ActivationRequest{}, ErrIdempotencyConflict
	}
	if created {
		if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'admin','market_maker.activation_proposed','market_maker_activation',$2,$3,jsonb_build_object('action',$4::text))`, actorID, request.ID, input.Reason, input.Action); err != nil {
			return ActivationRequest{}, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('market_maker.activation_proposed','market_maker_activation',$1,jsonb_build_object('request_id',$1::text,'action',$2::text))`, request.ID, request.Action); err != nil {
			return ActivationRequest{}, err
		}
	}
	return request, tx.Commit(ctx)
}

func (s *ActivationService) Approve(ctx context.Context, actorID, requestID string) (ActivationRequest, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ActivationRequest{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('limiance:market_maker_activation'))`); err != nil {
		return ActivationRequest{}, err
	}
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN user_roles r ON r.user_id=u.id WHERE u.id=$1 AND u.status='active' AND r.role='treasury_approver')`, actorID).Scan(&allowed); err != nil {
		return ActivationRequest{}, err
	}
	if !allowed {
		return ActivationRequest{}, ErrActivationRole
	}
	var request ActivationRequest
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT id::text,action,status,proposed_by::text,COALESCE(approved_by::text,''),created_at,payload FROM market_maker_activation_requests WHERE id=$1 FOR UPDATE`, requestID).Scan(&request.ID, &request.Action, &request.Status, &request.ProposedBy, &request.ApprovedBy, &request.CreatedAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActivationRequest{}, ErrActivationState
	}
	if err != nil {
		return ActivationRequest{}, err
	}
	if request.Status != "pending" {
		return request, tx.Commit(ctx)
	}
	if request.ProposedBy == actorID {
		return ActivationRequest{}, ErrSameChecker
	}
	var input ActivationInput
	if err = json.Unmarshal(payload, &input); err != nil {
		return ActivationRequest{}, err
	}
	if request.Action == "configure_dry_run" {
		err = s.applyDryRun(ctx, tx, request.ID, input)
	} else {
		err = s.applyLive(ctx, tx, input)
	}
	if err != nil {
		return ActivationRequest{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE market_maker_activation_requests SET status='approved',approved_by=$2,decided_at=now() WHERE id=$1`, request.ID, actorID); err != nil {
		return ActivationRequest{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata) VALUES($1,'admin','market_maker.activation_approved','market_maker_activation',$2,$3,jsonb_build_object('maker',$4::text,'action',$5::text))`, actorID, request.ID, input.Reason, request.ProposedBy, request.Action); err != nil {
		return ActivationRequest{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload) VALUES('market_maker.activation_approved','market_maker_activation',$1,jsonb_build_object('request_id',$1::text,'action',$2::text))`, request.ID, request.Action); err != nil {
		return ActivationRequest{}, err
	}
	request.Status = "approved"
	request.ApprovedBy = actorID
	return request, tx.Commit(ctx)
}

func (s *ActivationService) applyDryRun(ctx context.Context, tx pgx.Tx, requestID string, input ActivationInput) error {
	var userID, accountID string
	err := tx.QueryRow(ctx, `SELECT u.id::text,(array_agg(a.id))[1]::text FROM users u JOIN accounts a ON a.user_id=u.id AND a.kind='uta' AND a.status='active' WHERE lower(u.email)=lower($1) AND u.status='active' GROUP BY u.id HAVING count(a.id)=1`, input.MarketMakerEmail).Scan(&userID, &accountID)
	if err != nil {
		return fmt.Errorf("%w: dedicated active user with one active UTA required", ErrActivationState)
	}
	if _, err = tx.Exec(ctx, `UPDATE market_maker_configs SET enabled=FALSE,updated_at=now()`); err != nil {
		return err
	}
	for _, p := range input.Pairs {
		tag, e := tx.Exec(ctx, `UPDATE market_maker_configs SET enabled=TRUE,spread_bps=$2,quantity_atomic=$3::numeric,max_base_inventory_atomic=$4::numeric,max_quote_notional_atomic=$5::numeric,max_daily_loss_atomic=$6::numeric,max_divergence_bps=$7,stale_after_seconds=$8,updated_at=now() WHERE pair=$1`, strings.ToUpper(p.Pair), p.SpreadBPS, p.QuantityAtomic, p.MaxBaseInventoryAtomic, p.MaxQuoteNotionalAtomic, p.MaxDailyLossAtomic, p.MaxDivergenceBPS, p.StaleAfterSeconds)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrActivationState
		}
	}
	for _, g := range input.Inventory {
		var assetID, journalID string
		var sourceOK bool
		if err = tx.QueryRow(ctx, `SELECT (array_agg(id))[1]::text FROM assets WHERE symbol=$1 AND status='enabled' GROUP BY symbol HAVING count(*)=1`, strings.ToUpper(g.Asset)).Scan(&assetID); err != nil {
			return ErrActivationState
		}
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM accounts WHERE id=$1 AND kind='system' AND status='active')`, g.SourceAccountID).Scan(&sourceOK); err != nil || !sourceOK {
			return ErrActivationState
		}
		if err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(CASE direction WHEN 'credit' THEN amount_atomic ELSE -amount_atomic END),0) >= $3::numeric FROM postings po JOIN journals j ON j.id=po.journal_id AND j.status='posted' WHERE po.account_id=$1 AND po.asset_id=$2 AND po.bucket='available'`, g.SourceAccountID, assetID, g.AmountAtomic).Scan(&sourceOK); err != nil || !sourceOK {
			return fmt.Errorf("%w: insufficient approved treasury inventory", ErrActivationState)
		}
		if err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id) VALUES($1,'market_maker_inventory',$2) RETURNING id::text`, `market-maker-inventory:`+requestID+":"+assetID, requestID).Scan(&journalID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic) VALUES($1,$2,$3,'available','debit',$4::numeric),($1,$5,$3,'available','credit',$4::numeric)`, journalID, g.SourceAccountID, assetID, g.AmountAtomic, accountID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO market_maker_inventory_journals(request_id,asset_id,journal_id,amount_atomic) VALUES($1,$2,$3,$4::numeric)`, requestID, assetID, journalID, g.AmountAtomic); err != nil {
			return err
		}
	}
	var inventoryComplete bool
	if err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM market_maker_configs c JOIN trading_pairs p ON p.symbol=c.pair WHERE c.enabled AND (NOT EXISTS(SELECT 1 FROM market_maker_inventory_journals i WHERE i.request_id=$1 AND i.asset_id=p.base_asset_id) OR NOT EXISTS(SELECT 1 FROM market_maker_inventory_journals i WHERE i.request_id=$1 AND i.asset_id=p.quote_asset_id)))`, requestID).Scan(&inventoryComplete); err != nil || !inventoryComplete {
		return fmt.Errorf("%w: every pair requires approved base and quote inventory", ErrActivationState)
	}
	_, err = tx.Exec(ctx, `UPDATE market_maker_control SET enabled=TRUE,dry_run=TRUE,kill_switch=FALSE,user_id=$1,account_id=$2,updated_at=now() WHERE singleton=TRUE`, userID, accountID)
	return err
}

func (s *ActivationService) applyLive(ctx context.Context, tx pgx.Tx, input ActivationInput) error {
	if !input.ReferenceEvidence || !input.EmergencyStopTested || !input.LedgerReconciled || !input.OrderLifecycleTested {
		return ErrActivationState
	}
	var ready bool
	err := tx.QueryRow(ctx, `SELECT enabled AND dry_run AND NOT kill_switch AND user_id IS NOT NULL AND account_id IS NOT NULL AND EXISTS(SELECT 1 FROM market_maker_configs WHERE enabled) FROM market_maker_control WHERE singleton=TRUE`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return ErrActivationState
	}
	_, err = tx.Exec(ctx, `UPDATE market_maker_control SET dry_run=FALSE,updated_at=now() WHERE singleton=TRUE`)
	return err
}

func (s *ActivationService) EmergencyStop(ctx context.Context, actorID, reason string) error {
	if len(strings.TrimSpace(reason)) < 8 {
		return ErrActivationInput
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_roles WHERE user_id=$1 AND role IN ('treasury_operator','treasury_approver'))`, actorID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrActivationRole
	}
	if _, err = tx.Exec(ctx, `UPDATE market_maker_control SET kill_switch=TRUE,updated_at=now() WHERE singleton=TRUE`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason) VALUES($1,'admin','market_maker.emergency_stopped','market_maker_control','singleton',$2)`, actorID, strings.TrimSpace(reason)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
