package withdrawals

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/limiance/backend/internal/security/envelope"
)

var ErrTravelRuleUnavailable = errors.New("travel rule is unavailable")

type TravelRuleInput struct {
	OriginatorName     string `json:"originator_name"`
	OriginatorAddress  string `json:"originator_address"`
	BeneficiaryName    string `json:"beneficiary_name"`
	BeneficiaryAddress string `json:"beneficiary_address"`
	BeneficiaryCountry string `json:"beneficiary_country"`
	TransferPurpose    string `json:"transfer_purpose"`
}

func (s *Service) SaveTravelRule(ctx context.Context, userID, withdrawalID, encryptionKey string, input TravelRuleInput) error {
	input.OriginatorName = strings.TrimSpace(input.OriginatorName)
	input.OriginatorAddress = strings.TrimSpace(input.OriginatorAddress)
	input.BeneficiaryName = strings.TrimSpace(input.BeneficiaryName)
	input.BeneficiaryAddress = strings.TrimSpace(input.BeneficiaryAddress)
	input.BeneficiaryCountry = strings.ToUpper(strings.TrimSpace(input.BeneficiaryCountry))
	input.TransferPurpose = strings.TrimSpace(input.TransferPurpose)
	if userID == "" || withdrawalID == "" || input.OriginatorName == "" || input.OriginatorAddress == "" || input.BeneficiaryName == "" || input.BeneficiaryAddress == "" || len(input.BeneficiaryCountry) != 2 || input.TransferPurpose == "" {
		return ErrInvalidInput
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ErrInvalidInput
	}
	ciphertext, err := envelope.Seal(encryptionKey, string(payload))
	if err != nil {
		return ErrTravelRuleUnavailable
	}
	return s.data.SaveWithdrawalTravelRule(ctx, userID, withdrawalID, ciphertext)
}
