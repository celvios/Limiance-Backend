package testmoney

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/limiance/backend/internal/ledger"
)

// A single control lock serializes low-volume administrative issuance and quota
// consumption across policy versions. No matching or customer hot path uses it.
func loadPolicy(ctx context.Context, tx pgx.Tx) (string, Policy, error) {
	var enabled bool
	var environment, id string
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT c.enabled,c.environment,COALESCE(c.policy_id::text,''),COALESCE(p.policy,'{}'::jsonb)
 FROM test_money_control c LEFT JOIN test_money_policies p ON p.id=c.policy_id
 WHERE c.singleton=TRUE FOR UPDATE OF c`).Scan(&enabled, &environment, &id, &raw)
	if err != nil {
		return "", Policy{}, err
	}
	if !enabled || environment != "staging" || id == "" {
		return "", Policy{}, ErrDisabled
	}
	var p Policy
	if err = json.Unmarshal(raw, &p); err != nil {
		return "", p, ErrInput
	}
	return id, p, p.Validate()
}

func role(ctx context.Context, tx pgx.Tx, actor, wanted string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN user_roles r ON r.user_id=u.id
 WHERE u.id=$1 AND u.status='active' AND r.role=$2 FOR SHARE OF u,r`, actor, wanted).Scan(&id)
	if err == pgx.ErrNoRows {
		return ErrRole
	}
	return err
}

func eligible(ctx context.Context, tx pgx.Tx, p Policy, in Input) error {
	ap, ok := p.Assets[in.AssetID]
	if !ok {
		return ErrInput
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT a.id::text FROM accounts a JOIN users u ON u.id=a.user_id
 JOIN test_money_recipients r ON r.user_id=u.id JOIN assets asset ON asset.id=$3
 WHERE u.id=$1 AND a.id=$2 AND u.status='active' AND a.status='active'
 AND a.kind IN ('funding','uta') AND r.enabled AND asset.status='enabled'
 AND asset.network=$4 AND asset.decimals=$5 FOR SHARE OF a,u,r,asset`,
		in.RecipientID, in.AccountID, in.AssetID, ap.Network, ap.Decimals).Scan(&id)
	if err == pgx.ErrNoRows {
		return ErrRecipient
	}
	return err
}

func loadRequest(ctx context.Context, tx pgx.Tx, id string) (Request, error) {
	var r Request
	err := tx.QueryRow(ctx, `SELECT id::text,proposed_by::text,policy_id::text,recipient_id::text,account_id::text,asset_id::text,amount_atomic::text,reason,idempotency_key
 FROM test_money_requests WHERE id=$1`, id).Scan(&r.ID, &r.ProposedBy, &r.PolicyID, &r.Input.RecipientID, &r.Input.AccountID, &r.Input.AssetID, &r.Input.AmountAtomic, &r.Input.Reason, &r.Input.IdempotencyKey)
	if err == pgx.ErrNoRows {
		return r, ErrInput
	}
	return r, err
}

func record(ctx context.Context, tx pgx.Tx, actor, action, id, reason string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,actor_type,action,resource_type,resource_id,reason,metadata)
 VALUES($1,'admin',$2,'test_money_request',$3,$4,$5)`, actor, action, id, reason, raw)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox_events(event_type,aggregate_type,aggregate_id,payload)
 VALUES($1,'test_money_request',$2,$3)`, action, id, raw)
	return err
}

func postGrant(ctx context.Context, tx pgx.Tx, r Request, actor, key, value string, observations []Observation) (Grant, error) {
	var counterpart string
	// Frozen, unowned counterpart cannot be a normal transfer or activation source.
	_, err := tx.Exec(ctx, `INSERT INTO accounts(user_id,kind,name,status)
 VALUES(NULL,'system',$1,'frozen') ON CONFLICT (name) WHERE kind='system' DO NOTHING`,
		"test-money-issuance-"+r.Input.AssetID)
	if err != nil {
		return Grant{}, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM accounts WHERE kind='system' AND name=$1
 AND user_id IS NULL AND status='frozen' FOR SHARE`, "test-money-issuance-"+r.Input.AssetID).Scan(&counterpart)
	if err != nil {
		return Grant{}, err
	}
	accounts := []string{counterpart, r.Input.AccountID}
	sort.Strings(accounts)
	for _, id := range accounts {
		if err = ledger.LockAccountAsset(ctx, tx, id, r.Input.AssetID); err != nil {
			return Grant{}, err
		}
	}
	grant := Grant{RequestID: r.ID, ValueUSDTAtomic: value}
	err = tx.QueryRow(ctx, `INSERT INTO journals(idempotency_key,reference_type,reference_id)
 VALUES($1,'test_money_issuance',$2) RETURNING id::text`, "test-money:"+r.ID, r.ID).Scan(&grant.JournalID)
	if err != nil {
		return Grant{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO postings(journal_id,account_id,asset_id,bucket,direction,amount_atomic)
 VALUES($1,$2,$3,'available','debit',$4::numeric),($1,$5,$3,'available','credit',$4::numeric)`,
		grant.JournalID, counterpart, r.Input.AssetID, r.Input.AmountAtomic, r.Input.AccountID)
	if err != nil {
		return Grant{}, err
	}
	evidence, err := json.Marshal(observations)
	if err != nil {
		return Grant{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO test_money_grants(request_id,approved_by,approval_key,journal_id,value_usdt_atomic,evidence)
 VALUES($1,$2,$3,$4,$5::numeric,$6)`, r.ID, actor, key, grant.JournalID, value, evidence)
	if err != nil {
		return Grant{}, err
	}
	if err = record(ctx, tx, actor, "test_money.issued", r.ID, r.Input.Reason, grant); err != nil {
		return Grant{}, err
	}
	return grant, nil
}
