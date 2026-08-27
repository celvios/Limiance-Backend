package accounts

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/security/envelope"
	"github.com/limiance/backend/internal/security/password"
	"github.com/limiance/backend/internal/security/verification"
)

var (
	ErrInvalidInput = errors.New("invalid registration input")
	ErrEmailInUse   = errors.New("email is already registered")
)

type RegisterInput struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	CountryCode string `json:"country_code"`
}

type User struct {
	ID      string
	UID     int64
	Email   string
	Status  string
	Funding string
	UTA     string
}

type Balance struct {
	AccountID       string `json:"account_id"`
	AccountKind     string `json:"account_kind"`
	AccountName     string `json:"account_name"`
	AssetSymbol     string `json:"asset_symbol"`
	Network         string `json:"network"`
	AvailableAtomic string `json:"available_atomic"`
	HeldAtomic      string `json:"held_atomic"`
	PendingAtomic   string `json:"pending_atomic"`
	LockedAtomic    string `json:"locked_atomic"`
}

type Account struct {
	ID   string `json:"account_id"`
	Kind string `json:"account_kind"`
	Name string `json:"account_name"`
}

type Subaccount = datamanager.SubaccountSummary

type TransactionHistoryItem = datamanager.TransactionHistoryItem
type Service struct {
	data               *datamanager.Manager
	verificationPepper string
	encryptionKey      string
}

func NewService(data *datamanager.Manager, verificationPepper, encryptionKey string) *Service {
	return &Service{data: data, verificationPepper: verificationPepper, encryptionKey: encryptionKey}
}

func (s *Service) Balances(ctx context.Context, userID string) ([]Balance, error) {
	stored, err := s.data.AccountBalances(ctx, userID)
	if err != nil {
		return nil, err
	}
	balances := make([]Balance, 0, len(stored))
	for _, balance := range stored {
		balances = append(balances, Balance{
			AccountID:       balance.AccountID,
			AccountKind:     balance.AccountKind,
			AccountName:     balance.AccountName,
			AssetSymbol:     balance.AssetSymbol,
			Network:         balance.Network,
			AvailableAtomic: balance.AvailableAtomic,
			HeldAtomic:      balance.HeldAtomic,
			PendingAtomic:   balance.PendingAtomic,
			LockedAtomic:    balance.LockedAtomic,
		})
	}
	return balances, nil
}

func (s *Service) Accounts(ctx context.Context, userID string) ([]Account, error) {
	stored, err := s.data.UserAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	accounts := make([]Account, 0, len(stored))
	for _, account := range stored {
		accounts = append(accounts, Account{ID: account.ID, Kind: account.Kind, Name: account.Name})
	}
	return accounts, nil
}

func (s *Service) CreateSubaccount(ctx context.Context, userID, name string) (Subaccount, error) {
	name = strings.TrimSpace(name)
	if len(name) < 3 || len(name) > 50 {
		return Subaccount{}, ErrInvalidInput
	}
	return s.data.CreateSubaccount(ctx, userID, name)
}

func (s *Service) Subaccounts(ctx context.Context, userID string) ([]Subaccount, error) {
	return s.data.UserSubaccounts(ctx, userID)
}

func (s *Service) SubaccountBalances(ctx context.Context, userID, accountID string) ([]Balance, error) {
	stored, err := s.data.SubaccountBalances(ctx, userID, accountID)
	if err != nil {
		return nil, err
	}
	balances := make([]Balance, 0, len(stored))
	for _, balance := range stored {
		balances = append(balances, Balance{AccountID: balance.AccountID, AccountKind: balance.AccountKind, AccountName: balance.AccountName, AssetSymbol: balance.AssetSymbol, Network: balance.Network, AvailableAtomic: balance.AvailableAtomic, HeldAtomic: balance.HeldAtomic, PendingAtomic: balance.PendingAtomic, LockedAtomic: balance.LockedAtomic})
	}
	return balances, nil
}

func (s *Service) TransactionHistory(ctx context.Context, userID, accountKind string, limit int, cursor int64) ([]TransactionHistoryItem, error) {
	return s.data.AccountTransactionHistory(ctx, userID, accountKind, limit, cursor)
}

func (s *Service) Register(ctx context.Context, input RegisterInput) (User, error) {
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.CountryCode = strings.ToUpper(strings.TrimSpace(input.CountryCode))
	if _, err := mail.ParseAddress(input.Email); err != nil || len(input.CountryCode) != 2 || !validPassword(input.Password) {
		return User{}, ErrInvalidInput
	}
	hash, err := password.Hash(input.Password)
	if err != nil {
		return User{}, err
	}
	code, err := verification.NewCode()
	if err != nil {
		return User{}, err
	}
	codeHash, err := verification.Hash(s.verificationPepper, code)
	if err != nil {
		return User{}, err
	}
	sealedCode, err := envelope.Seal(s.encryptionKey, code)
	if err != nil {
		return User{}, err
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	var user User
	err = s.data.WithinTransaction(ctx, func(tx *datamanager.Transaction) error {
		created, err := tx.Accounts().CreateUser(ctx, input.Email, hash, input.CountryCode)
		if err != nil {
			return err
		}
		user.ID, user.UID, user.Email, user.Status = created.ID, created.UID, created.Email, created.Status
		user.Funding, user.UTA, err = tx.Accounts().CreateDefaultAccounts(ctx, user.ID)
		if err != nil {
			return err
		}
		metadata := map[string]string{"country_code": input.CountryCode}
		if err := tx.Audit().Record(ctx, "system", "user.registered", "user", user.ID, metadata); err != nil {
			return err
		}
		if err := tx.Verifications().CreateEmailChallenge(ctx, user.ID, codeHash, expiresAt); err != nil {
			return err
		}
		return tx.Outbox().Enqueue(ctx, "email.verification_requested", "user", user.ID, map[string]any{"user_id": user.ID, "email": user.Email, "code_ciphertext": sealedCode, "expires_at": expiresAt.Format(time.RFC3339)})
	})
	if err != nil {
		if strings.Contains(err.Error(), "users_email_key") {
			return User{}, ErrEmailInUse
		}
		return User{}, err
	}
	return user, nil
}

func validPassword(value string) bool {
	if len(value) < 12 || len(value) > 128 {
		return false
	}
	var upper, lower, digit bool
	for _, r := range value {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return upper && lower && digit
}
