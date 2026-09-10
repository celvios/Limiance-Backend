// staging-market-maker-control exercises the deployed emergency-stop boundary
// and restores reference-only evaluation through the existing maker-checker flow.
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
	flag.StringVar(&opt.action, "action", "status", "status, stop, propose-resume, or approve-resume")
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
	case "approve-resume":
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
	case "approve-resume":
		if !opt.confirmStaging || opt.actorEmail == "" || opt.requestID == "" {
			return errors.New("approval requires confirmation, actor and request ID")
		}
	default:
		return errors.New("unsupported action")
	}
	return nil
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
