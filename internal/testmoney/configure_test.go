package testmoney

import (
	"errors"
	"testing"
)

func TestPostgresConfigurePilotIsAuditedAndIdempotent(t *testing.T) {
	f := databaseFixture(t)
	input := f.pilotInput(t, "pilot-config-1")
	beforePolicies := f.count(t, `SELECT count(*) FROM test_money_policies`)
	first, err := f.service.ConfigurePilot(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.ConfigurePilot(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.PolicyID == "" || second.PolicyID != first.PolicyID || first.GlobalLimit != TesterLimitUSDTAtomic || first.Assets["ISSUANCE_TEST"] != f.asset || first.Recipients[input.RecipientEmails[0]] != f.user {
		t.Fatalf("unexpected results: first=%#v second=%#v", first, second)
	}
	if got := f.count(t, `SELECT count(*) FROM test_money_policies`); got != beforePolicies+1 {
		t.Fatalf("idempotent retry created policy: got %d", got)
	}
	if got := f.count(t, `SELECT count(*) FROM audit_events WHERE action='test_money.pilot_configured'`); got != 1 {
		t.Fatalf("audit count: %d", got)
	}
	if got := f.count(t, `SELECT count(*) FROM outbox_events WHERE event_type='test_money.pilot_configured'`); got != 1 {
		t.Fatalf("outbox count: %d", got)
	}
	var enabled, ready bool
	var environment, policyID string
	if err = f.pool.QueryRow(f.ctx, `SELECT enabled,withdrawal_limits_ready,environment,policy_id::text FROM test_money_control`).Scan(&enabled, &ready, &environment, &policyID); err != nil {
		t.Fatal(err)
	}
	if !enabled || !ready || environment != "staging" || policyID != first.PolicyID {
		t.Fatalf("unexpected control: enabled=%v ready=%v env=%s policy=%s", enabled, ready, environment, policyID)
	}
}

func TestPostgresConfigurePilotRejectsConflictsRolesAndProduction(t *testing.T) {
	f := databaseFixture(t)
	input := f.pilotInput(t, "pilot-config-2")
	if _, err := f.service.ConfigurePilot(f.ctx, input); err != nil {
		t.Fatal(err)
	}
	conflict := input
	conflict.Reason = "different reviewed pilot reason"
	if _, err := f.service.ConfigurePilot(f.ctx, conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	wrongRole := input
	wrongRole.IdempotencyKey = "pilot-config-role"
	wrongRole.OperatorEmail, wrongRole.ApproverEmail = wrongRole.ApproverEmail, wrongRole.OperatorEmail
	if _, err := f.service.ConfigurePilot(f.ctx, wrongRole); !errors.Is(err, ErrRole) {
		t.Fatalf("expected role error, got %v", err)
	}
	production := NewService(f.pool, "production", fixedReferences{})
	input.IdempotencyKey = "pilot-config-production"
	if _, err := production.ConfigurePilot(f.ctx, input); !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected disabled, got %v", err)
	}
}

func (f *fixture) pilotInput(t *testing.T, key string) PilotConfigInput {
	t.Helper()
	var operatorEmail, approverEmail, recipientEmail string
	if err := f.pool.QueryRow(f.ctx, `SELECT email::text FROM users WHERE id=$1`, f.maker).Scan(&operatorEmail); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT email::text FROM users WHERE id=$1`, f.checker).Scan(&approverEmail); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT email::text FROM users WHERE id=$1`, f.user).Scan(&recipientEmail); err != nil {
		t.Fatal(err)
	}
	return PilotConfigInput{OperatorEmail: operatorEmail, ApproverEmail: approverEmail, RecipientEmails: []string{recipientEmail}, AssetSymbols: []string{"issuance_test"}, Reason: "reviewed staging pilot configuration", IdempotencyKey: key}
}
