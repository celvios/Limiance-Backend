package datamanager

import (
	"context"
	"errors"
	"math/big"

	"github.com/jackc/pgx/v5"
)

var ErrWithdrawalDepositLimit = errors.New("withdrawal exceeds remaining same-token deposit entitlement")

type withdrawalEntitlement struct {
	enforced  bool
	credited  string
	committed string
}

// Lock order: issuance control (shared), user/asset entitlement, account/balance.
// The shared control lock also orders first issuance against withdrawal admission.
// Turning issuance off cannot remove the limit after a grant has been recorded.
func checkWithdrawalEntitlement(ctx context.Context, tx pgx.Tx, userID, assetID string, amount int64) (withdrawalEntitlement, error) {
	var result withdrawalEntitlement
	err := tx.QueryRow(ctx, `SELECT withdrawal_limits_ready OR enabled
 FROM test_money_control WHERE singleton=TRUE FOR SHARE`).Scan(&result.enforced)
	if err != nil {
		return result, err
	}
	// Read history after the control lock, using a fresh READ COMMITTED snapshot.
	if !result.enforced {
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM test_money_grants)`).Scan(&result.enforced)
		if err != nil || !result.enforced {
			return result, err
		}
	}
	if amount < 0 {
		return result, ErrWithdrawalDepositLimit
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "withdrawal-entitlement:"+userID+":"+assetID); err != nil {
		return result, err
	}
	// One deposit is counted once, even if a corrupt duplicate journal exists.
	// Require actual credited, confirmed deposit identity plus balanced postings.
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(d.amount_atomic),0)::text FROM deposits d
 WHERE d.user_id=$1 AND d.asset_id=$2 AND d.status='credited' AND d.risk_status='approved'
 AND d.confirmations>=d.confirmations_required AND d.transaction_hash<>''
 AND EXISTS(SELECT 1 FROM journals j WHERE j.reference_type='deposit_credit'
 AND j.reference_id=d.id::text AND j.status='posted'
 AND NOT EXISTS(SELECT 1 FROM postings p WHERE p.journal_id=j.id AND p.asset_id<>d.asset_id)
 AND (SELECT COALESCE(SUM(p.amount_atomic),0) FROM postings p JOIN accounts a ON a.id=p.account_id
      WHERE p.journal_id=j.id AND p.direction='credit' AND p.asset_id=d.asset_id AND a.user_id=d.user_id)=d.amount_atomic
 AND (SELECT COALESCE(SUM(p.amount_atomic),0) FROM postings p WHERE p.journal_id=j.id AND p.direction='credit')=d.amount_atomic
 AND (SELECT COALESCE(SUM(p.amount_atomic),0) FROM postings p WHERE p.journal_id=j.id AND p.direction='debit')=d.amount_atomic)`, userID, assetID).Scan(&result.credited)
	if err != nil {
		return result, err
	}
	// A missing provider ID is not proof of non-submission (an ACK may be lost).
	// Terminal releases require the custody lifecycle's compensating journal.
	err = tx.QueryRow(ctx, `SELECT COALESCE(SUM(w.amount_atomic),0)::text FROM withdrawals w
 WHERE w.user_id=$1 AND w.asset_id=$2 AND (
 w.status NOT IN ('failed','cancelled','rejected') OR
 NOT EXISTS(
  SELECT 1 FROM journals j WHERE j.withdrawal_id=w.id AND j.status='posted'
  AND j.reference_type IN ('withdrawal_release','withdrawal_cancel')))`, userID, assetID).Scan(&result.committed)
	if err != nil {
		return result, err
	}
	credited, ok := new(big.Int).SetString(result.credited, 10)
	if !ok {
		return result, ErrWithdrawalDepositLimit
	}
	committed, ok := new(big.Int).SetString(result.committed, 10)
	if !ok {
		return result, ErrWithdrawalDepositLimit
	}
	if new(big.Int).Add(committed, big.NewInt(amount)).Cmp(credited) > 0 {
		return result, ErrWithdrawalDepositLimit
	}
	return result, nil
}

func recordWithdrawalEntitlement(ctx context.Context, tx pgx.Tx, check withdrawalEntitlement, id, userID, assetID string, amount int64) error {
	if !check.enforced {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO withdrawal_entitlement_checks(withdrawal_id,user_id,asset_id,amount_atomic,credited_deposits_atomic,prior_commitments_atomic)
 VALUES($1,$2,$3,$4,$5::numeric,$6::numeric)`, id, userID, assetID, amount, check.credited, check.committed)
	return err
}
