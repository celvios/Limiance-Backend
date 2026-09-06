package datamanager

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type WithdrawalReconciliationCandidate struct {
	WithdrawalID string
	Provider     string
}

type WithdrawalReconciliationObservation struct {
	Found                 bool
	ProviderTransactionID string
	ExternalID            string
	Status                string
	TransactionHash       string
}

func (m *Manager) WithdrawalReconciliationCandidates(ctx context.Context, provider string, limit int) ([]WithdrawalReconciliationCandidate, error) {
	if provider == "" {
		return nil, ErrWithdrawalDispatchChanged
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := m.pool.Query(ctx, `SELECT r.withdrawal_id::text,r.provider
		FROM custody_capacity_reservations r
		WHERE r.provider=$1
		AND NOT EXISTS(SELECT 1 FROM custody_capacity_terminal_events t WHERE t.withdrawal_id=r.withdrawal_id)
		AND NOT EXISTS(SELECT 1 FROM withdrawal_reconciliation_observations o
			WHERE o.withdrawal_id=r.withdrawal_id AND o.observed_at>now()-interval '30 seconds')
		ORDER BY r.created_at,r.withdrawal_id LIMIT $2`, provider, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []WithdrawalReconciliationCandidate
	for rows.Next() {
		var candidate WithdrawalReconciliationCandidate
		if err := rows.Scan(&candidate.WithdrawalID, &candidate.Provider); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func (m *Manager) RecordWithdrawalReconciliationObservation(ctx context.Context, candidate WithdrawalReconciliationCandidate, observation WithdrawalReconciliationObservation) (bool, error) {
	if candidate.WithdrawalID == "" || candidate.Provider == "" || observation.ExternalID != candidate.WithdrawalID ||
		(observation.Found && (observation.ProviderTransactionID == "" || strings.TrimSpace(observation.Status) == "")) ||
		(!observation.Found && (observation.ProviderTransactionID != "" || observation.Status != "" || observation.TransactionHash != "")) {
		return false, ErrWithdrawalDispatchChanged
	}
	tx, err := m.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var provider string
	err = tx.QueryRow(ctx, `SELECT d.provider FROM withdrawal_dispatches d
		JOIN custody_capacity_reservations r ON r.withdrawal_id=d.withdrawal_id
		WHERE d.withdrawal_id=$1 FOR SHARE OF d,r`, candidate.WithdrawalID).Scan(&provider)
	if errors.Is(err, pgx.ErrNoRows) || provider != candidate.Provider {
		return false, ErrWithdrawalDispatchChanged
	}
	if err != nil {
		return false, err
	}
	var terminal bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM custody_capacity_terminal_events WHERE withdrawal_id=$1)`, candidate.WithdrawalID).Scan(&terminal); err != nil {
		return false, err
	}
	if terminal {
		return false, tx.Commit(ctx)
	}
	_, err = tx.Exec(ctx, `INSERT INTO withdrawal_reconciliation_observations
		(withdrawal_id,provider,found,provider_transaction_id,provider_status,transaction_hash)
		VALUES($1,$2,$3,$4,$5,$6)`, candidate.WithdrawalID, candidate.Provider, observation.Found,
		observation.ProviderTransactionID, strings.ToUpper(strings.TrimSpace(observation.Status)), observation.TransactionHash)
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
