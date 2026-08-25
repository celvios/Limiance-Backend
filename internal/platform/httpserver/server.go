package httpserver

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/limiance/backend/internal/accounts"
	"github.com/limiance/backend/internal/admin"
	"github.com/limiance/backend/internal/assets"
	"github.com/limiance/backend/internal/auth"
	"github.com/limiance/backend/internal/config"
	"github.com/limiance/backend/internal/conversions"
	"github.com/limiance/backend/internal/custody"
	"github.com/limiance/backend/internal/datamanager"
	"github.com/limiance/backend/internal/deposits"
	"github.com/limiance/backend/internal/kyc"
	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/notifications"
	"github.com/limiance/backend/internal/phone"
	"github.com/limiance/backend/internal/transfers"
	"github.com/limiance/backend/internal/withdrawals"
)

func NewServer(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	data := datamanager.New(pool)
	mux.HandleFunc("GET /readyz", ready(data))
	mux.HandleFunc("GET /v1", apiIndex)
	mux.HandleFunc("POST /v1/webhooks/sumsub", sumsubWebhook(cfg.SumsubWebhookKey))
	if pool != nil {
		if verifier, err := custody.NewWebhookVerifier(cfg.FireblocksJWKSURL, nil); err == nil {
			mux.HandleFunc("POST /v1/webhooks/fireblocks", fireblocksWebhook(verifier, data))
		}
		accountService := accounts.NewService(data, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		accountHandler := NewAccountHandler(accountService, logger)
		profileHandler := NewProfileHandler(data, accountService)
		mux.HandleFunc("POST /v1/auth/register", accountHandler.Register)
		authService := auth.NewService(data, cfg.SessionTTL, cfg.TOTPEncryptionKey, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		emailVerificationService := auth.NewEmailVerificationService(data, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		authHandler := NewAuthHandler(authService, logger, cfg.Environment != "development").WithEmailVerification(emailVerificationService)
		mux.HandleFunc("POST /v1/auth/login", authHandler.Login)
		mux.HandleFunc("POST /v1/auth/totp/verify", authHandler.VerifyTOTPLogin)
		mux.HandleFunc("POST /v1/auth/password-reset/request", authHandler.PasswordResetRequest)
		mux.HandleFunc("POST /v1/auth/password-reset/confirm", authHandler.PasswordResetConfirm)
		mux.HandleFunc("POST /v1/auth/mfa/recover", authHandler.MFARecoveryRequest)
		emailVerificationHandler := NewEmailVerificationHandler(emailVerificationService, logger)
		mux.HandleFunc("POST /v1/auth/email/verify", emailVerificationHandler.Verify)
		mux.HandleFunc("POST /v1/auth/email/resend", emailVerificationHandler.Resend)
		mux.Handle("GET /v1/auth/session", requireSession(authService)(http.HandlerFunc(authHandler.Session)))
		mux.Handle("POST /v1/auth/logout", requireSession(authService)(http.HandlerFunc(authHandler.Logout)))
		mux.Handle("POST /v1/auth/freeze", requireSession(authService)(http.HandlerFunc(authHandler.Freeze)))
		mux.Handle("POST /v1/auth/mfa/step-up", requireSession(authService)(http.HandlerFunc(authHandler.StepUp)))
		mux.Handle("GET /v1/security/sessions", requireSession(authService)(http.HandlerFunc(authHandler.Sessions)))
		mux.Handle("DELETE /v1/security/sessions/{session_id}", requireSession(authService)(http.HandlerFunc(authHandler.RevokeSession)))
		// Customer-profile aliases are kept alongside the security routes to give
		// the frontend a stable, user-centred API without duplicating state.
		mux.Handle("GET /v1/user/sessions", requireSession(authService)(http.HandlerFunc(authHandler.Sessions)))
		mux.Handle("DELETE /v1/user/sessions/{session_id}", requireSession(authService)(http.HandlerFunc(authHandler.RevokeSession)))
		mux.Handle("PUT /v1/security/anti-phishing-code", requireSession(authService)(http.HandlerFunc(authHandler.SetAntiPhishingCode)))
		mux.Handle("POST /v1/security/totp/enroll", requireSession(authService)(http.HandlerFunc(authHandler.TOTPEnroll)))
		mux.Handle("POST /v1/security/totp/confirm", requireSession(authService)(http.HandlerFunc(authHandler.TOTPConfirm)))
		mux.Handle("DELETE /v1/security/totp", requireSession(authService)(http.HandlerFunc(authHandler.TOTPDisable)))
		var twilioProvider *notifications.TwilioVerify
		if provider, err := notifications.NewTwilioVerify(notifications.TwilioVerifyConfig{APIKey: cfg.TwilioAPIKey, APISecret: cfg.TwilioAPISecret, ServiceSID: cfg.TwilioVerifySID}); err == nil {
			twilioProvider = provider
		}
		phoneHandler := NewPhoneHandler(phone.NewService(data, twilioProvider), logger)
		mux.Handle("POST /v1/auth/phone/start", requireSession(authService)(http.HandlerFunc(phoneHandler.Start)))
		mux.Handle("POST /v1/auth/phone/verify", requireSession(authService)(http.HandlerFunc(phoneHandler.Verify)))
		mux.Handle("GET /v1/accounts/balances", requireSession(authService)(http.HandlerFunc(accountHandler.Balances)))
		mux.Handle("GET /v1/wallet/balances", requireSession(authService)(http.HandlerFunc(accountHandler.WalletBalances)))
		mux.Handle("GET /v1/accounts/transactions", requireSession(authService)(http.HandlerFunc(accountHandler.Transactions)))
		mux.Handle("GET /v1/user/profile", requireSession(authService)(http.HandlerFunc(profileHandler.Get)))
		mux.Handle("PUT /v1/user/preferences", requireSession(authService)(http.HandlerFunc(profileHandler.Preferences)))
		var sumsubProvider kyc.SessionProvider
		if provider, err := kyc.NewClient(kyc.ClientConfig{AppToken: cfg.SumsubAppToken, SecretKey: cfg.SumsubSecretKey, LevelName: cfg.SumsubLevelName}); err == nil {
			sumsubProvider = provider
		}
		kycHandler := NewKYCHandler(kyc.NewSessionService(data, sumsubProvider, cfg.SumsubLevelName), kyc.NewService(data), logger)
		mux.Handle("POST /v1/kyc/sessions", requireSession(authService)(http.HandlerFunc(kycHandler.CreateSession)))
		mux.Handle("GET /v1/kyc/status", requireSession(authService)(http.HandlerFunc(kycHandler.Status)))
		transferHandler := NewTransferHandler(transfers.NewService(data), logger)
		mux.Handle("POST /v1/transfers", requireSession(authService)(http.HandlerFunc(transferHandler.Create)))
		mux.Handle("POST /v1/transfers/recipients/resolve", requireSession(authService)(http.HandlerFunc(transferHandler.ResolveRecipient)))
		if marketProvider, err := marketdata.NewBybit(cfg.BybitMarketDataBaseURL, nil); err == nil {
			conversionHandler := NewConversionHandler(conversions.NewService(data, marketProvider), logger)
			marketHandler := NewMarketHandler(marketProvider)
			mux.Handle("POST /v1/conversions/quotes", requireSession(authService)(http.HandlerFunc(conversionHandler.Quote)))
			mux.Handle("POST /v1/conversions/confirm", requireSession(authService)(http.HandlerFunc(conversionHandler.Confirm)))
			mux.Handle("POST /v1/conversions/{quote_id}/execute", requireSession(authService)(http.HandlerFunc(conversionHandler.Confirm)))
			mux.Handle("GET /v1/conversions", requireSession(authService)(http.HandlerFunc(conversionHandler.History)))
			mux.Handle("GET /v1/market/prices", requireSession(authService)(http.HandlerFunc(marketHandler.Prices)))
		}
		var custodyProvider custody.Provider
		switch cfg.CustodyMode {
		case "self_custody_testnet":
			if provider, err := custody.NewSelfCustodyTestnetClient(cfg.SelfCustodySignerURL, nil); err == nil {
				custodyProvider = provider
			}
		default:
			if provider, err := custody.NewFireblocksClient(custody.FireblocksConfig{APIKey: cfg.FireblocksAPIKey, PrivateKey: cfg.FireblocksPrivateKey, BaseURL: cfg.FireblocksBaseURL}); err == nil {
				custodyProvider = provider
			}
		}
		depositHandler := NewDepositHandler(custody.NewService(data, custodyProvider, custody.RoutePolicy{Mode: cfg.CustodyMode, SelfCustodyTestnetEnabled: cfg.SelfCustodyTestnetEnabled}), logger)
		mux.Handle("POST /v1/deposits/addresses", requireSession(authService)(http.HandlerFunc(depositHandler.Address)))
		mux.Handle("POST /v1/wallet/deposit-addresses", requireSession(authService)(http.HandlerFunc(depositHandler.Address)))
		depositHistoryHandler := NewDepositHistoryHandler(deposits.NewHistoryService(data), logger)
		mux.Handle("GET /v1/deposits", requireSession(authService)(http.HandlerFunc(depositHistoryHandler.List)))
		withdrawalHandler := NewWithdrawalHandler(withdrawals.NewService(data), logger, cfg.WithdrawalAddressCooldown, cfg.TravelRuleEncryptionKey)
		mux.Handle("POST /v1/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.Request)))
		mux.Handle("GET /v1/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.History)))
		mux.Handle("POST /v1/wallet/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.Request)))
		mux.Handle("GET /v1/wallet/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.History)))
		mux.Handle("POST /v1/withdrawals/{withdrawal_id}/cancel", requireSession(authService)(http.HandlerFunc(withdrawalHandler.Cancel)))
		mux.Handle("POST /v1/withdrawals/{withdrawal_id}/travel-rule", requireSession(authService)(http.HandlerFunc(withdrawalHandler.SaveTravelRule)))
		mux.Handle("GET /v1/withdrawal-addresses", requireSession(authService)(http.HandlerFunc(withdrawalHandler.ListAddresses)))
		mux.Handle("POST /v1/withdrawal-addresses", requireSession(authService)(http.HandlerFunc(withdrawalHandler.AddAddress)))
		mux.Handle("DELETE /v1/withdrawal-addresses/{address_id}", requireSession(authService)(http.HandlerFunc(withdrawalHandler.DisableAddress)))
		notificationHandler := NewNotificationHandler(notifications.NewService(data), logger)
		mux.Handle("POST /v1/notifications", requireSession(authService)(http.HandlerFunc(notificationHandler.Create)))
		mux.Handle("GET /v1/notifications", requireSession(authService)(http.HandlerFunc(notificationHandler.List)))
		mux.Handle("PATCH /v1/notifications/{notification_id}/read", requireSession(authService)(http.HandlerFunc(notificationHandler.MarkRead)))
		mux.Handle("PATCH /v1/notifications/read-all", requireSession(authService)(http.HandlerFunc(notificationHandler.MarkAllRead)))
		mux.Handle("POST /v1/notifications/{notification_id}/read", requireSession(authService)(http.HandlerFunc(notificationHandler.MarkRead)))
		mux.Handle("DELETE /v1/notifications/{notification_id}/read", requireSession(authService)(http.HandlerFunc(notificationHandler.MarkUnread)))
		mux.Handle("POST /v1/notification-devices", requireSession(authService)(http.HandlerFunc(notificationHandler.RegisterDevice)))
		mux.Handle("POST /v1/notifications/device-token", requireSession(authService)(http.HandlerFunc(notificationHandler.RegisterDevice)))
		adminHandler := NewAdminHandler(admin.NewService(data), logger)
		mux.Handle("POST /v1/admin/deposits/{deposit_id}/approve", requireSession(authService)(http.HandlerFunc(adminHandler.ApproveDeposit)))
		mux.Handle("POST /v1/admin/withdrawals/{withdrawal_id}/approvals", requireSession(authService)(http.HandlerFunc(adminHandler.ApproveWithdrawal)))
		mux.Handle("GET /v1/admin/operational-controls/withdrawals", requireSession(authService)(http.HandlerFunc(adminHandler.WithdrawalControl)))
		mux.Handle("PUT /v1/admin/operational-controls/withdrawals", requireSession(authService)(http.HandlerFunc(adminHandler.WithdrawalControl)))
		mux.Handle("GET /v1/admin/users/{user_id}/roles", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("PUT /v1/admin/users/{user_id}/roles/{role}", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("DELETE /v1/admin/users/{user_id}/roles/{role}", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("PUT /v1/admin/conversion-pairs", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionPair)))
		mux.Handle("GET /v1/admin/operational-controls/conversions", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionControl)))
		mux.Handle("PUT /v1/admin/operational-controls/conversions", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionControl)))
		assetHandler := NewAssetHandler(assets.NewService(data), logger)
		mux.HandleFunc("GET /v1/assets/catalog", assetHandler.Catalog)
	}

	handler := recoverer(logger)(requestID(logger)(securityHeaders(customerCORS(cfg.AllowedBrowserOrigins, csrfOriginCheck(cfg.AllowedBrowserOrigins, mux)))))
	return &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           handler,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    16 << 10,
	}
}
