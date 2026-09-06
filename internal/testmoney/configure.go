package testmoney

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

const (
	pilotReferenceMaxAgeSeconds = 30
	pilotMaxDivergenceBPS       = 100
)

type PilotConfigInput struct {
	OperatorEmail   string
	ApproverEmail   string
	RecipientEmails []string
	AssetSymbols    []string
	Reason          string
	IdempotencyKey  string
}

type PilotConfigResult struct {
	PolicyID    string            `json:"policy_id"`
	OperatorID  string            `json:"operator_id"`
	ApproverID  string            `json:"approver_id"`
	Recipients  map[string]string `json:"recipients"`
	Assets      map[string]string `json:"assets"`
	GlobalLimit string            `json:"global_limit_usdt_atomic"`
}

// ConfigurePilot is a cold-path staging operation. It enables no recipient or
// asset implicitly: both lists are explicit, normalized and included in the
// idempotency hash and audit evidence.
func (s *Service) ConfigurePilot(ctx context.Context, input PilotConfigInput) (PilotConfigResult, error) {
	input.OperatorEmail = strings.ToLower(strings.TrimSpace(input.OperatorEmail))
	input.ApproverEmail = strings.ToLower(strings.TrimSpace(input.ApproverEmail))
	input.RecipientEmails = normalizePilotList(input.RecipientEmails, true)
	input.AssetSymbols = normalizePilotList(input.AssetSymbols, false)
	if input.OperatorEmail == "" || input.ApproverEmail == "" || input.OperatorEmail == input.ApproverEmail || len(input.RecipientEmails) == 0 || len(input.AssetSymbols) == 0 || len(strings.TrimSpace(input.Reason)) < 8 || len(input.Reason) > 1000 || !validKey(input.IdempotencyKey) {
		return PilotConfigResult{}, ErrInput
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return PilotConfigResult{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('staging-test-money-pilot',0))`); err != nil {
		return PilotConfigResult{}, err
	}
	result := PilotConfigResult{Recipients: map[string]string{}, Assets: map[string]string{}}
	if err = pilotActor(ctx, tx, input.OperatorEmail, "treasury_operator", &result.OperatorID); err != nil {
		return PilotConfigResult{}, err
	}
	if err = pilotActor(ctx, tx, input.ApproverEmail, "treasury_approver", &result.ApproverID); err != nil {
		return PilotConfigResult{}, err
	}
	if result.OperatorID == result.ApproverID {
		return PilotConfigResult{}, ErrChecker
	}
	for _, email := range input.RecipientEmails {
		var id string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE lower(email)=lower($1) AND status='active' FOR SHARE`, email).Scan(&id); err == pgx.ErrNoRows {
			return PilotConfigResult{}, ErrRecipient
		} else if err != nil {
			return PilotConfigResult{}, err
		}
		result.Recipients[email] = id
	}
	policy := Policy{
		GlobalLimitUSDTAtomic: new(big.Int).Mul(mustAtomic(TesterLimitUSDTAtomic), big.NewInt(int64(len(result.Recipients)))).String(),
		MaxAgeSeconds:         pilotReferenceMaxAgeSeconds,
		MaxDivergenceBPS:      pilotMaxDivergenceBPS,
		Assets:                map[string]AssetPolicy{},
	}
	for _, symbol := range input.AssetSymbols {
		var id, network string
		var decimals int
		err = tx.QueryRow(ctx, `SELECT id::text,network,decimals FROM assets WHERE symbol=$1 AND network='internal_spot' AND status='enabled'`, symbol).Scan(&id, &network, &decimals)
		if err == pgx.ErrNoRows {
			return PilotConfigResult{}, ErrInput
		}
		if err != nil {
			return PilotConfigResult{}, err
		}
		policy.Assets[id] = AssetPolicy{Network: network, Decimals: decimals}
		result.Assets[symbol] = id
	}
	if err = policy.Validate(); err != nil {
		return PilotConfigResult{}, err
	}
	result.GlobalLimit = policy.GlobalLimitUSDTAtomic
	requestBody, _ := json.Marshal(struct {
		Input  PilotConfigInput
		Result PilotConfigResult
	}{input, result})
	requestHash := sha256.Sum256(requestBody)
	var priorHash string
	var priorResult []byte
	err = tx.QueryRow(ctx, `SELECT metadata->>'request_hash',metadata->'result' FROM audit_events
	 WHERE actor_id=$1 AND action='test_money.pilot_configured' AND metadata->>'idempotency_key'=$2
	 ORDER BY occurred_at LIMIT 1`, result.OperatorID, input.IdempotencyKey).Scan(&priorHash, &priorResult)
	if err == nil {
		if priorHash != hex.EncodeToString(requestHash[:]) || json.Unmarshal(priorResult, &result) != nil {
			return PilotConfigResult{}, ErrConflict
		}
		return result, tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return PilotConfigResult{}, err
	}
	policyBody, _ := json.Marshal(policy)
	if err = tx.QueryRow(ctx, `INSERT INTO test_money_policies(policy) VALUES($1) RETURNING id::text`, policyBody).Scan(&result.PolicyID); err != nil {
		return PilotConfigResult{}, err
	}
	for _, userID := range result.Recipients {
		if _, err = tx.Exec(ctx, `INSERT INTO test_money_recipients(user_id,enabled) VALUES($1,TRUE)
		 ON CONFLICT(user_id) DO UPDATE SET enabled=TRUE`, userID); err != nil {
			return PilotConfigResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE test_money_control SET enabled=TRUE,withdrawal_limits_ready=TRUE,environment='staging',policy_id=$1 WHERE singleton=TRUE`, result.PolicyID); err != nil {
		return PilotConfigResult{}, err
	}
	metadata, _ := json.Marshal(map[string]any{"idempotency_key": input.IdempotencyKey, "request_hash": hex.EncodeToString(requestHash[:]), "approver_id": result.ApproverID, "result": result})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata)
	 VALUES($1,'admin','test_money.pilot_configured','test_money_policy',$2,$3,$4)`, result.OperatorID, result.PolicyID, input.Reason, metadata); err != nil {
		return PilotConfigResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload)
	 VALUES('test_money.pilot_configured','test_money_policy',$1,$2)`, result.PolicyID, metadata); err != nil {
		return PilotConfigResult{}, err
	}
	return result, tx.Commit(ctx)
}

func normalizePilotList(values []string, email bool) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if email {
			value = strings.ToLower(value)
		} else {
			value = strings.ToUpper(value)
		}
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func pilotActor(ctx context.Context, tx pgx.Tx, email, wantedRole string, id *string) error {
	err := tx.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles r ON r.user_id=u.id
	 WHERE lower(u.email)=lower($1) AND u.status='active' AND r.role=$2 FOR SHARE OF u,r`, email, wantedRole).Scan(id)
	if err == pgx.ErrNoRows {
		return ErrRole
	}
	return err
}

func mustAtomic(value string) *big.Int {
	n, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid compile-time atomic amount")
	}
	return n
}
