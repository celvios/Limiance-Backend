package admin

import (
	"context"
	"errors"
	"strings"

	"github.com/limiance/backend/internal/datamanager"
)

var ErrInvalidApproval = errors.New("invalid deposit approval")
var ErrInvalidWithdrawalApproval = errors.New("invalid withdrawal approval")
var ErrNotPlatformAdministrator = errors.New("platform administrator role is required")
var ErrInvalidRole = errors.New("invalid role")

var allowedRoles = map[string]struct{}{"support": {}, "compliance": {}, "treasury_operator": {}, "treasury_approver": {}, "auditor": {}, "platform_administrator": {}}

type DepositApproval struct {
	Reason string `json:"reason"`
}

type Service struct{ data *datamanager.Manager }

func NewService(data *datamanager.Manager) *Service { return &Service{data: data} }

func (s *Service) ApproveDeposit(ctx context.Context, reviewerID, depositID string, input DepositApproval) (datamanager.TransferResult, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if depositID == "" || len(input.Reason) < 8 || len(input.Reason) > 500 {
		return datamanager.TransferResult{}, ErrInvalidApproval
	}
	return s.data.CreditApprovedDeposit(ctx, reviewerID, depositID, input.Reason)
}

func (s *Service) ApproveWithdrawal(ctx context.Context, approverID, withdrawalID string, input DepositApproval) (datamanager.WithdrawalApprovalResult, error) {
	input.Reason = strings.TrimSpace(input.Reason)
	if withdrawalID == "" || len(input.Reason) < 8 || len(input.Reason) > 500 {
		return datamanager.WithdrawalApprovalResult{}, ErrInvalidWithdrawalApproval
	}
	return s.data.ApproveWithdrawal(ctx, approverID, withdrawalID, input.Reason)
}

func (s *Service) SetWithdrawalsEnabled(ctx context.Context, actorID string, enabled bool) error {
	allowed, err := s.data.HasRole(ctx, actorID, "platform_administrator")
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotPlatformAdministrator
	}
	return s.data.SetWithdrawalsEnabled(ctx, actorID, enabled)
}

func (s *Service) WithdrawalsEnabledForAdministrator(ctx context.Context, actorID string) (bool, error) {
	if actorID == "" {
		return false, ErrNotPlatformAdministrator
	}
	allowed, err := s.data.HasRole(ctx, actorID, "platform_administrator")
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, ErrNotPlatformAdministrator
	}
	return s.data.WithdrawalsEnabled(ctx)
}

func (s *Service) UserRoles(ctx context.Context, actorID, userID string) ([]datamanager.UserRole, error) {
	allowed, err := s.data.HasRole(ctx, actorID, "platform_administrator")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotPlatformAdministrator
	}
	return s.data.UserRoles(ctx, userID)
}

func (s *Service) AuditEvents(ctx context.Context, actorID string, limit int, cursor, action, resourceType, resourceID string) ([]datamanager.AuditEvent, error) {
	allowed, err := s.data.HasRole(ctx, actorID, "auditor")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotPlatformAdministrator
	}
	return s.data.AuditEvents(ctx, limit, strings.TrimSpace(cursor), strings.TrimSpace(action), strings.TrimSpace(resourceType), strings.TrimSpace(resourceID))
}

func (s *Service) SetUserRole(ctx context.Context, actorID, userID, role string, grant bool) (bool, error) {
	if _, ok := allowedRoles[role]; !ok || userID == "" {
		return false, ErrInvalidRole
	}
	changed, err := s.data.SetUserRole(ctx, actorID, userID, role, grant)
	if errors.Is(err, datamanager.ErrAdministratorRoleRequired) {
		return false, ErrNotPlatformAdministrator
	}
	return changed, err
}

func (s *Service) SetConversionPair(ctx context.Context, actorID string, input datamanager.ConversionPairPolicyInput) error {
	input.FromSymbol = strings.ToUpper(strings.TrimSpace(input.FromSymbol))
	input.ToSymbol = strings.ToUpper(strings.TrimSpace(input.ToSymbol))
	input.FromNetwork = strings.TrimSpace(input.FromNetwork)
	input.ToNetwork = strings.TrimSpace(input.ToNetwork)
	input.MarketSymbol = strings.ToUpper(strings.TrimSpace(input.MarketSymbol))
	if input.FromSymbol == "" || input.ToSymbol == "" || input.MarketSymbol == "" {
		return ErrInvalidRole
	}
	return s.data.SetConversionPairPolicy(ctx, actorID, input)
}

func (s *Service) SetConversionsEnabled(ctx context.Context, actorID string, enabled bool) error {
	return s.data.SetConversionsEnabled(ctx, actorID, enabled)
}
func (s *Service) ConversionsEnabledForAdministrator(ctx context.Context, actorID string) (bool, error) {
	allowed, err := s.data.HasRole(ctx, actorID, "platform_administrator")
	if err != nil {
		return false, err
	}
	if !allowed {
		return false, ErrNotPlatformAdministrator
	}
	return s.data.ConversionsEnabled(ctx)
}

func (s *Service) KYCApplications(ctx context.Context, actorID, status string) ([]datamanager.KYCApplication, error) {
	allowed, err := s.data.HasRole(ctx, actorID, "compliance")
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, ErrNotPlatformAdministrator
	}
	return s.data.KYCApplications(ctx, strings.ToLower(strings.TrimSpace(status)))
}

func (s *Service) KYCApplication(ctx context.Context, actorID, userID string) (datamanager.KYCApplication, error) {
	allowed, err := s.data.HasRole(ctx, actorID, "compliance")
	if err != nil {
		return datamanager.KYCApplication{}, err
	}
	if !allowed {
		return datamanager.KYCApplication{}, ErrNotPlatformAdministrator
	}
	return s.data.KYCApplication(ctx, strings.TrimSpace(userID))
}

func (s *Service) ReviewKYC(ctx context.Context, actorID, userID, status, reason string) error {
	allowed, err := s.data.HasRole(ctx, actorID, "compliance")
	if err != nil {
		return err
	}
	if !allowed {
		return ErrNotPlatformAdministrator
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "approved" && status != "rejected" {
		return ErrInvalidRole
	}
	reason = strings.TrimSpace(reason)
	if status == "rejected" && len(reason) < 8 {
		return ErrInvalidApproval
	}
	tier := int16(0)
	if status == "approved" {
		tier = 1
	}
	return s.data.ReviewKYC(ctx, actorID, userID, status, map[bool]string{true: "GREEN", false: "RED"}[status == "approved"], reason, tier)
}
