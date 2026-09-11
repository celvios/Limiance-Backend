// staging-market-maker-control exercises the deployed emergency-stop boundary
// and drives tightly scoped staging activation transitions through the existing
// maker-checker flow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/marketmaker"
	"github.com/limiance/backend/internal/platform/database"
)

type options struct {
	action, actorEmail, reason, idempotencyKey, requestID string
	confirmStaging                                        bool
}

type result struct {
	Action            string `json:"action"`
	RequestID         string `json:"request_id,omitempty"`
	RequestStatus     string `json:"request_status,omitempty"`
	Enabled           bool   `json:"enabled"`
	DryRun            bool   `json:"dry_run"`
	KillSwitch        bool   `json:"kill_switch"`
	LatestStopAt      string `json:"latest_stop_at,omitempty"`
	CommandsAfterStop int64  `json:"commands_after_stop"`
}

func main() {
	var opt options
	flag.StringVar(&opt.action, "action", "status", "status, stop, propose-resume, propose-funded-dry-run, approve-resume, or approve-request")
	flag.StringVar(&opt.actorEmail, "actor-email", "", "active operator or approver email")
	flag.StringVar(&opt.reason, "reason", "", "audited reason")
	flag.StringVar(&opt.idempotencyKey, "idempotency-key", "", "payload-bound idempotency key")
	flag.StringVar(&opt.requestID, "request-id", "", "resume proposal UUID")
	flag.BoolVar(&opt.confirmStaging, "confirm-staging", false, "confirm a staging mutation")
	flag.Parse()
	opt.action = strings.ToLower(strings.TrimSpace(opt.action))
	if err := validateOptions(opt); err != nil {
		fatal(err)
	}
	cfg := config.Load()
	if cfg.Environment != "staging" {
		fatal(errors.New("command is restricted to staging"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()
	service := marketmaker.NewActivationService(pool)
	out := result{Action: opt.action}
	switch opt.action {
	case "status":
	case "stop":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "")
		if err == nil {
			err = service.EmergencyStop(ctx, actorID, opt.reason)
		}
	case "propose-resume":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_operator")
		if err == nil {
			var input marketmaker.ActivationInput
			input, err = latestReferenceOnly(ctx, pool)
			if err == nil {
				input.Reason, input.IdempotencyKey = opt.reason, opt.idempotencyKey
				var request marketmaker.ActivationRequest
				request, err = service.Propose(ctx, actorID, input)
				out.RequestID, out.RequestStatus = request.ID, request.Status
			}
		}
	case "propose-funded-dry-run":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_operator")
		if err == nil {
			var input marketmaker.ActivationInput
			input, err = fundedDryRun(ctx, pool)
			if err == nil {
				input.Reason, input.IdempotencyKey = opt.reason, opt.idempotencyKey
				var request marketmaker.ActivationRequest
				request, err = service.Propose(ctx, actorID, input)
				out.RequestID, out.RequestStatus = request.ID, request.Status
			}
		}
	case "approve-resume", "approve-request":
		var actorID string
		actorID, err = resolveActor(ctx, pool, opt.actorEmail, "treasury_approver")
		if err == nil {
			var request marketmaker.ActivationRequest
			request, err = service.Approve(ctx, actorID, opt.requestID)
			out.RequestID, out.RequestStatus = request.ID, request.Status
		}
	}
	if err != nil {
		fatal(err)
	}
	if err = loadStatus(ctx, pool, &out); err != nil {
		fatal(err)
	}
	if err = json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fatal(err)
	}
}

func validateOptions(opt options) error {
	switch opt.action {
	case "status":
		return nil
	case "stop":
		if !opt.confirmStaging || opt.actorEmail == "" || len(strings.TrimSpace(opt.reason)) < 8 {
			return errors.New("stop requires --confirm-staging, --actor-email and an audited reason")
		}
	case "propose-resume":
		if !opt.confirmStaging || opt.actorEmail == "" || len(strings.TrimSpace(opt.reason)) < 8 || opt.idempotencyKey == "" {
			return errors.New("proposal requires confirmation, actor, reason and idempotency key")
		}
	case "propose-funded-dry-run":
		if !opt.confirmStaging || opt.actorEmail == "" || len(strings.TrimSpace(opt.reason)) < 8 || opt.idempotencyKey == "" {
			return errors.New("funded dry-run proposal requires confirmation, actor, reason and idempotency key")
		}
	case "approve-resume", "approve-request":
		if !opt.confirmStaging || opt.actorEmail == "" || opt.requestID == "" {
			return errors.New("approval requires confirmation, actor and request ID")
		}
	default:
		return errors.New("unsupported action")
	}
	return nil
}

func fundedDryRun(ctx context.Context, pool *pgxpool.Pool) (marketmaker.ActivationInput, error) {
	input, err := latestReferenceOnly(ctx, pool)
	if err != nil {
		return marketmaker.ActivationInput{}, err
	}
	pairs := make([]string, 0, len(input.Pairs))
	for _, policy := range input.Pairs {
		pairs = append(pairs, strings.ToUpper(strings.TrimSpace(policy.Pair)))
	}
	var matched int
	if err = pool.QueryRow(ctx, `SELECT count(*)::int FROM trading_pairs WHERE symbol=ANY($1::text[])`, pairs).Scan(&matched); err != nil {
		return marketmaker.ActivationInput{}, err
	}
	if matched != len(pairs) {
		return marketmaker.ActivationInput{}, errors.New("reference-only policy contains an unknown pair")
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT a.symbol,a.network,a.status,t.id::text,
			(SELECT COALESCE(SUM(CASE p.direction WHEN 'credit' THEN p.amount_atomic ELSE -p.amount_atomic END),0)::text
			 FROM postings p JOIN journals j ON j.id=p.journal_id AND j.status='posted'
			 WHERE p.account_id=t.id AND p.asset_id=a.id AND p.bucket='available')
		FROM trading_pairs tp
		JOIN assets a ON a.id=tp.base_asset_id OR a.id=tp.quote_asset_id
		JOIN accounts t ON t.kind='system' AND t.name='staging-market-maker-treasury' AND t.status='active'
		WHERE tp.symbol=ANY($1::text[])
		ORDER BY a.symbol`, pairs)
	if err != nil {
		return marketmaker.ActivationInput{}, err
	}
	defer rows.Close()
	input.Action = "configure_dry_run"
	input.Inventory = nil
	for rows.Next() {
		var grant marketmaker.InventoryGrant
		var assetStatus string
		if err = rows.Scan(&grant.Asset, &grant.Network, &assetStatus, &grant.SourceAccountID, &grant.AmountAtomic); err != nil {
			return marketmaker.ActivationInput{}, err
		}
		if grant.Network != "internal_spot" || assetStatus != "enabled" {
			return marketmaker.ActivationInput{}, fmt.Errorf("required market asset %s is not an enabled internal spot asset", grant.Asset)
		}
		if !positiveAmount(grant.AmountAtomic) {
			return marketmaker.ActivationInput{}, fmt.Errorf("approved treasury inventory is missing for %s", grant.Asset)
		}
		input.Inventory = append(input.Inventory, grant)
	}
	if err = rows.Err(); err != nil {
		return marketmaker.ActivationInput{}, err
	}
	if len(input.Inventory) == 0 {
		return marketmaker.ActivationInput{}, errors.New("approved treasury inventory is empty")
	}
	return input, nil
}

func positiveAmount(value string) bool {
	for i, char := range value {
		if char < '0' || char > '9' || (i == 0 && char == '0') {
			return false
		}
	}
	return value != ""
}

func resolveActor(ctx context.Context, pool *pgxpool.Pool, email, role string) (string, error) {
	var id string
	query := `SELECT u.id::text FROM users u WHERE lower(u.email)=lower($1) AND u.status='active'`
	args := []any{email}
	if role != "" {
		query += ` AND EXISTS(SELECT 1 FROM user_roles r WHERE r.user_id=u.id AND r.role=$2)`
		args = append(args, role)
	}
	if err := pool.QueryRow(ctx, query, args...).Scan(&id); err != nil {
		return "", fmt.Errorf("active actor with required role not found: %w", err)
	}
	return id, nil
}

func latestReferenceOnly(ctx context.Context, pool *pgxpool.Pool) (marketmaker.ActivationInput, error) {
	var payload []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM market_maker_activation_requests WHERE action='configure_reference_only' AND status='approved' ORDER BY decided_at DESC,id DESC LIMIT 1`).Scan(&payload); err != nil {
		return marketmaker.ActivationInput{}, err
	}
	var input marketmaker.ActivationInput
	if err := json.Unmarshal(payload, &input); err != nil {
		return marketmaker.ActivationInput{}, err
	}
	if input.Action != "configure_reference_only" {
		return marketmaker.ActivationInput{}, errors.New("latest approved payload is not reference-only")
	}
	return input, nil
}

func loadStatus(ctx context.Context, pool *pgxpool.Pool, out *result) error {
	var stoppedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT enabled,dry_run,kill_switch,(SELECT max(occurred_at) FROM audit_events WHERE action='market_maker.emergency_stopped') FROM market_maker_control WHERE singleton=TRUE`).Scan(&out.Enabled, &out.DryRun, &out.KillSwitch, &stoppedAt); err != nil {
		return err
	}
	if stoppedAt == nil {
		return nil
	}
	out.LatestStopAt = stoppedAt.UTC().Format(time.RFC3339Nano)
	return pool.QueryRow(ctx, `SELECT count(*)::bigint FROM market_maker_command_log WHERE created_at > $1`, *stoppedAt).Scan(&out.CommandsAfterStop)
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
