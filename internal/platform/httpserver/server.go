package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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
	"github.com/limiance/backend/internal/fees"
	"github.com/limiance/backend/internal/kyc"
	"github.com/limiance/backend/internal/marketdata"
	"github.com/limiance/backend/internal/notifications"
	"github.com/limiance/backend/internal/observability"
	"github.com/limiance/backend/internal/phone"
	"github.com/limiance/backend/internal/pnl"
	"github.com/limiance/backend/internal/security/geetest"
	"github.com/limiance/backend/internal/trading"
	tradingpostgres "github.com/limiance/backend/internal/trading/postgres"
	"github.com/limiance/backend/internal/trading/transport"
	"github.com/limiance/backend/internal/transfers"
	"github.com/limiance/backend/internal/withdrawals"
)

func NewServer(cfg config.Config, logger *slog.Logger, pool *pgxpool.Pool) *http.Server {
	mux := http.NewServeMux()
	backgroundContext, stopBackground := context.WithCancel(context.Background())
	var shutdownClosers []func()
	metrics := observability.NewMetrics()
	mux.Handle("GET /metrics", metrics.Handler())
	mux.HandleFunc("GET /healthz", health)
	data := datamanager.New(pool)
	mux.HandleFunc("GET /readyz", ready(data))
	mux.HandleFunc("GET /v1", apiIndex)
	if pool != nil {
		if verifier, err := custody.NewWebhookVerifier(cfg.FireblocksJWKSURL, nil); err == nil {
			mux.HandleFunc("POST /v1/webhooks/fireblocks", fireblocksWebhook(verifier, data))
		}
		accountService := accounts.NewService(data, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		var feeCache fees.Cache
		if cfg.RedisURL != "" {
			redisFeeCache, cacheErr := fees.NewRedisCache(cfg.RedisURL)
			if cacheErr != nil {
				logger.Error("Redis fee cache disabled", "error", cacheErr)
			} else {
				feeCache = redisFeeCache
			}
		}
		feeService := fees.NewService(data, feeCache)
		var apiLimiter endpointLimiter = unavailableEndpointLimiter{}
		if cfg.RedisURL != "" {
			redisLimiter, limitErr := newRedisEndpointLimiter(cfg.RedisURL)
			if limitErr != nil {
				logger.Error("Redis API rate limiter unavailable", "error", limitErr)
			} else {
				apiLimiter = redisLimiter
				shutdownClosers = append(shutdownClosers, func() { _ = redisLimiter.Close() })
			}
		}
		marketRepository := marketdata.NewPostgresStore(pool)
		marketHub := NewMarketHub()
		var marketCache marketdata.SnapshotCache = marketdata.NewMemoryStore()
		if cfg.RedisURL != "" {
			redisMarketStore, marketErr := marketdata.NewRedisStore(cfg.RedisURL)
			if marketErr != nil {
				logger.Error("Redis market data disabled", "error", marketErr)
			} else {
				marketCache = redisMarketStore
				shutdownClosers = append(shutdownClosers, func() { _ = redisMarketStore.Close() })
				go func() {
					if err := redisMarketStore.Subscribe(backgroundContext, marketHub.Publish); err != nil && backgroundContext.Err() == nil {
						logger.Error("market data Redis subscription stopped", "error", err)
					}
				}()
			}
		}
		marketV2Handler := NewMarketV2Handler(marketRepository, marketCache, marketHub, cfg.AllowedBrowserOrigins)
		publicMarketLimit := func(handler http.HandlerFunc) http.Handler {
			return endpointRateLimit(apiLimiter, "market.read", 1200, time.Minute, false, handler)
		}
		mux.Handle("GET /v2/market/markets", publicMarketLimit(marketV2Handler.Markets))
		mux.Handle("GET /v2/market/orderbook/{pair}", publicMarketLimit(marketV2Handler.OrderBook))
		mux.Handle("GET /v2/market/ticker/{pair}", publicMarketLimit(marketV2Handler.Ticker))
		mux.Handle("GET /v2/market/trades/{pair}", publicMarketLimit(marketV2Handler.Trades))
		mux.Handle("GET /v2/market/klines/{pair}", publicMarketLimit(marketV2Handler.Klines))
		mux.Handle("GET /v2/ws", endpointRateLimit(apiLimiter, "market.websocket", 60, time.Minute, false, http.HandlerFunc(marketV2Handler.WebSocket)))
		mux.Handle("GET /ws/v2/market", endpointRateLimit(apiLimiter, "market.websocket", 60, time.Minute, false, http.HandlerFunc(marketV2Handler.WebSocket)))
		accountHandler := NewAccountHandler(accountService, logger)
		profileHandler := NewProfileHandler(data, accountService)
		captchaVerifier := geetest.New(cfg.GeeTestCaptchaID, cfg.GeeTestPrivateKey)
		protectAuth := func(next http.Handler) http.Handler {
			if captchaVerifier == nil {
				return next
			}
			return captchaVerifier.Middleware(next)
		}
		protectAdminLogin := func(next http.Handler) http.Handler {
			protected := protectAuth(next)
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Origin") == "https://admin.celvios.site" {
					next.ServeHTTP(w, r)
					return
				}
				protected.ServeHTTP(w, r)
			})
		}
		mux.Handle("POST /v1/auth/register", protectAuth(http.HandlerFunc(accountHandler.Register)))
		authService := auth.NewService(data, cfg.SessionTTL, cfg.TOTPEncryptionKey, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		emailVerificationService := auth.NewEmailVerificationService(data, cfg.VerificationPepper, cfg.VerificationEncryptionKey)
		authHandler := NewAuthHandler(authService, logger, cfg.Environment != "development").WithEmailVerification(emailVerificationService)
		if cfg.GoogleClientID != "" && cfg.GoogleClientSecret != "" && cfg.GoogleRedirectURL != "" {
			if provider, err := auth.NewOIDCProvider(context.Background(), auth.OIDCProviderConfig{Name: "google", Issuer: "https://accounts.google.com", ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, RedirectURL: cfg.GoogleRedirectURL}); err == nil {
				oauthHandler := NewOAuthHandler(authHandler, data, provider)
				mux.Handle("GET /v1/auth/google/start", http.HandlerFunc(oauthHandler.Start))
				mux.Handle("GET /v1/auth/google/callback", http.HandlerFunc(oauthHandler.Callback))
			} else {
				logger.Error("google oidc disabled", "error", err)
			}
		}
		if cfg.TelegramClientID != "" && cfg.TelegramClientSecret != "" && cfg.TelegramRedirectURL != "" {
			if provider, err := auth.NewOIDCProvider(context.Background(), auth.OIDCProviderConfig{Name: "telegram", Issuer: "https://oauth.telegram.org", ClientID: cfg.TelegramClientID, ClientSecret: cfg.TelegramClientSecret, RedirectURL: cfg.TelegramRedirectURL}); err == nil {
				oauthHandler := NewOAuthHandler(authHandler, data, provider)
				mux.Handle("GET /v1/auth/telegram/start", http.HandlerFunc(oauthHandler.Start))
				mux.Handle("GET /v1/auth/telegram/callback", http.HandlerFunc(oauthHandler.Callback))
			} else {
				logger.Error("telegram oidc disabled", "error", err)
			}
		}
		mux.Handle("POST /v1/auth/login", protectAdminLogin(http.HandlerFunc(authHandler.Login)))
		mux.Handle("POST /v1/auth/subaccount/login", protectAdminLogin(http.HandlerFunc(authHandler.SubaccountLogin)))
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
		mux.Handle("POST /v1/security/step-up", requireSession(authService)(http.HandlerFunc(authHandler.StepUp)))
		mux.Handle("GET /v1/security/sessions", requireSession(authService)(http.HandlerFunc(authHandler.Sessions)))
		mux.Handle("DELETE /v1/security/sessions/{session_id}", requireSession(authService)(http.HandlerFunc(authHandler.RevokeSession)))
		apiKeyHandler := NewAPIKeyHandler(data, cfg.VerificationEncryptionKey)
		mux.Handle("GET /v1/security/api-keys", requireSession(authService)(http.HandlerFunc(apiKeyHandler.List)))
		mux.Handle("POST /v1/security/api-keys", requireSession(authService)(http.HandlerFunc(apiKeyHandler.Create)))
		mux.Handle("DELETE /v1/security/api-keys/{key_id}", requireSession(authService)(http.HandlerFunc(apiKeyHandler.Revoke)))
		// Customer-profile aliases are kept alongside the security routes to give
		// the frontend a stable, user-centred API without duplicating state.
		mux.Handle("GET /v1/user/sessions", requireSession(authService)(http.HandlerFunc(authHandler.Sessions)))
		mux.Handle("DELETE /v1/user/sessions/{session_id}", requireSession(authService)(http.HandlerFunc(authHandler.RevokeSession)))
		mux.Handle("DELETE /v1/user/sessions", requireSession(authService)(http.HandlerFunc(authHandler.RevokeOtherSessions)))
		mux.Handle("PUT /v1/security/anti-phishing-code", requireSession(authService)(http.HandlerFunc(authHandler.SetAntiPhishingCode)))
		mux.Handle("GET /v1/security/anti-phishing-code", requireSession(authService)(http.HandlerFunc(authHandler.AntiPhishingStatus)))
		mux.Handle("POST /v1/security/anti-phishing-code", requireSession(authService)(http.HandlerFunc(authHandler.GeneratedAntiPhishingCode)))
		mux.Handle("DELETE /v1/security/anti-phishing-code", requireSession(authService)(http.HandlerFunc(authHandler.GeneratedAntiPhishingCode)))
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
		mux.Handle("GET /v1/accounts/balances", requireAccountSession(authService)(http.HandlerFunc(accountHandler.Balances)))
		mux.Handle("GET /v1/wallet/balances", requireAccountSession(authService)(http.HandlerFunc(accountHandler.WalletBalances)))
		mux.Handle("GET /v1/accounts/transactions", requireAccountSession(authService)(http.HandlerFunc(accountHandler.Transactions)))
		mux.Handle("GET /v1/accounts/subaccounts", requireSession(authService)(http.HandlerFunc(accountHandler.Subaccounts)))
		mux.Handle("POST /v1/accounts/subaccounts", requireSession(authService)(http.HandlerFunc(accountHandler.Subaccounts)))
		mux.Handle("POST /v1/accounts/switch", requireSession(authService)(http.HandlerFunc(accountHandler.SwitchAccount)))
		mux.Handle("GET /v1/accounts/subaccounts/{account_id}/balances", requireAccountSession(authService)(http.HandlerFunc(accountHandler.SubaccountBalances)))
		mux.Handle("GET /v1/accounts/subaccounts/{account_id}", requireSession(authService)(http.HandlerFunc(accountHandler.SubaccountDetails)))
		mux.Handle("POST /v1/accounts/subaccounts/{account_id}/freeze", requireSession(authService)(http.HandlerFunc(accountHandler.SubaccountLifecycle)))
		mux.Handle("POST /v1/accounts/subaccounts/{account_id}/unfreeze", requireSession(authService)(http.HandlerFunc(accountHandler.SubaccountLifecycle)))
		mux.Handle("DELETE /v1/accounts/subaccounts/{account_id}", requireSession(authService)(http.HandlerFunc(accountHandler.SubaccountLifecycle)))
		mux.Handle("GET /v1/user/profile", requireSessionOrAPIKey(authService, data, cfg.VerificationEncryptionKey)(http.HandlerFunc(profileHandler.Get)))

		orderEngine, orderEngineErr := transport.NewRequestClient(context.Background(), cfg.MatchingEngineOrderURL, cfg.MatchingEngineTimeout)
		controlEngine, controlEngineErr := transport.NewRequestClient(context.Background(), cfg.MatchingEngineControlURL, cfg.MatchingEngineTimeout)
		if orderEngineErr != nil || controlEngineErr != nil {
			logger.Error("order gateway disabled", "order_engine_error", orderEngineErr, "control_engine_error", controlEngineErr)
			if orderEngine != nil {
				_ = orderEngine.Close()
			}
			if controlEngine != nil {
				_ = controlEngine.Close()
			}
		} else {
			orderService := trading.NewService(tradingpostgres.New(pool), cachedTradingFeeResolver{service: feeService}, orderEngine).WithControlEngine(controlEngine)
			orderHandler := NewOrderHandler(orderService, logger)
			orderAuth := requireAccountSessionOrAPIKey(authService, data, cfg.VerificationEncryptionKey)
			orderReadLimit := func(handler http.HandlerFunc) http.Handler {
				return endpointRateLimit(apiLimiter, "orders.read", 600, time.Minute, true, orderAuth(handler))
			}
			orderWriteLimit := func(handler http.HandlerFunc) http.Handler {
				return endpointRateLimit(apiLimiter, "orders.write", 120, time.Minute, true, orderAuth(handler))
			}
			mux.Handle("POST /v2/orders", orderWriteLimit(orderHandler.Place))
			mux.Handle("GET /v2/orders", orderReadLimit(orderHandler.List))
			mux.Handle("GET /v2/orders/history", orderReadLimit(orderHandler.History))
			mux.Handle("GET /v2/orders/{order_id}", orderReadLimit(orderHandler.Get))
			mux.Handle("DELETE /v2/orders/{order_id}", orderWriteLimit(orderHandler.Cancel))
		}
		mux.Handle("PUT /v1/user/preferences", requireSession(authService)(http.HandlerFunc(profileHandler.Preferences)))
		feesHandler := NewFeesHandler(feeService)
		limitsHandler := NewLimitsHandler(data)
		pnlHandler := NewPnLHandler(pnl.NewService(data), logger)
		mux.Handle("GET /v1/user/fees", requireSession(authService)(http.HandlerFunc(feesHandler.Get)))
		mux.Handle("GET /v1/user/limits", requireSession(authService)(http.HandlerFunc(limitsHandler.Get)))
		mux.Handle("GET /v1/user/pnl", requireSession(authService)(http.HandlerFunc(pnlHandler.Get)))
		mux.Handle("GET /v1/user/daily-pnl", requireSession(authService)(http.HandlerFunc(pnlHandler.Get)))
		mux.Handle("GET /v1/accounts/pnl", requireSession(authService)(http.HandlerFunc(pnlHandler.Get)))
		var sumsubProvider kyc.SessionProvider
		if provider, err := kyc.NewClient(kyc.ClientConfig{AppToken: cfg.SumsubAppToken, SecretKey: cfg.SumsubSecretKey, LevelName: cfg.SumsubLevelName}); err == nil {
			sumsubProvider = provider
		}
		kycStatusService := kyc.NewService(data)
		mux.HandleFunc("POST /v1/webhooks/sumsub", sumsubWebhook(cfg.SumsubWebhookKey, data, kycStatusService))
		kycHandler := NewKYCHandler(kyc.NewSessionService(data, sumsubProvider, cfg.SumsubLevelName), kycStatusService, logger)
		mux.Handle("POST /v1/kyc/sessions", requireSession(authService)(http.HandlerFunc(kycHandler.CreateSession)))
		mux.Handle("POST /v1/kyc/applications", requireSession(authService)(http.HandlerFunc(kycHandler.CreateSession)))
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
			} else {
				logger.Error("self-custody signer initialization failed", "error", err)
			}
		default:
			if provider, err := custody.NewFireblocksClient(custody.FireblocksConfig{APIKey: cfg.FireblocksAPIKey, PrivateKey: cfg.FireblocksPrivateKey, BaseURL: cfg.FireblocksBaseURL}); err == nil {
				custodyProvider = provider
			} else {
				logger.Error("fireblocks client initialization failed", "error", err)
			}
		}
		depositHandler := NewDepositHandler(custody.NewService(data, custodyProvider, custody.RoutePolicy{Mode: cfg.CustodyMode, SelfCustodyTestnetEnabled: cfg.SelfCustodyTestnetEnabled}), logger)
		mux.Handle("POST /v1/deposits/addresses", requireSession(authService)(http.HandlerFunc(depositHandler.Address)))
		mux.Handle("POST /v1/wallet/deposit-addresses", requireSession(authService)(http.HandlerFunc(depositHandler.Address)))
		depositHistoryHandler := NewDepositHistoryHandler(deposits.NewHistoryService(data), logger)
		mux.Handle("GET /v1/deposits", requireSession(authService)(http.HandlerFunc(depositHistoryHandler.List)))
		withdrawalHandler := NewWithdrawalHandler(withdrawals.NewService(data), logger, cfg.WithdrawalAddressCooldown, cfg.TravelRuleEncryptionKey)
		mux.Handle("POST /v1/withdrawals", requireSession(authService)(requireWithdrawalStepUp(authService, data, http.HandlerFunc(withdrawalHandler.Request))))
		mux.Handle("GET /v1/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.History)))
		mux.Handle("POST /v1/wallet/withdrawals", requireSession(authService)(requireWithdrawalStepUp(authService, data, http.HandlerFunc(withdrawalHandler.Request))))
		mux.Handle("GET /v1/wallet/withdrawals", requireSession(authService)(http.HandlerFunc(withdrawalHandler.History)))
		mux.Handle("POST /v1/withdrawals/{withdrawal_id}/cancel", requireSession(authService)(http.HandlerFunc(withdrawalHandler.Cancel)))
		mux.Handle("POST /v1/withdrawals/{withdrawal_id}/travel-rule", requireSession(authService)(http.HandlerFunc(withdrawalHandler.SaveTravelRule)))
		mux.Handle("GET /v1/withdrawal-addresses", requireSession(authService)(http.HandlerFunc(withdrawalHandler.ListAddresses)))
		mux.Handle("POST /v1/withdrawal-addresses", requireSession(authService)(requireStepUp(authService, "withdrawal_address_added", http.HandlerFunc(withdrawalHandler.AddAddress))))
		mux.Handle("DELETE /v1/withdrawal-addresses/{address_id}", requireSession(authService)(requireStepUp(authService, "withdrawal_address_deleted", http.HandlerFunc(withdrawalHandler.DisableAddress))))
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
		mux.Handle("GET /v1/admin/audit-events", requireSession(authService)(http.HandlerFunc(adminHandler.AuditEvents)))
		mux.Handle("POST /v1/admin/deposits/{deposit_id}/approve", requireSession(authService)(http.HandlerFunc(adminHandler.ApproveDeposit)))
		mux.Handle("POST /v1/admin/withdrawals/{withdrawal_id}/approvals", requireSession(authService)(http.HandlerFunc(adminHandler.ApproveWithdrawal)))
		mux.Handle("GET /v1/admin/operational-controls/withdrawals", requireSession(authService)(http.HandlerFunc(adminHandler.WithdrawalControl)))
		mux.Handle("PUT /v1/admin/operational-controls/withdrawals", requireSession(authService)(http.HandlerFunc(adminHandler.WithdrawalControl)))
		mux.Handle("GET /v1/admin/users/{user_id}/roles", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("PUT /v1/admin/users/{user_id}/roles/{role}", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("DELETE /v1/admin/users/{user_id}/roles/{role}", requireSession(authService)(http.HandlerFunc(adminHandler.UserRoles)))
		mux.Handle("PUT /v1/admin/conversion-pairs", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionPair)))
		mux.Handle("GET /v1/admin/kyc/applications", requireSession(authService)(http.HandlerFunc(adminHandler.KYCApplications)))
		mux.Handle("GET /v1/admin/kyc/pending", requireSession(authService)(http.HandlerFunc(adminHandler.KYCApplications)))
		mux.Handle("GET /v1/admin/kyc/{id}", requireSession(authService)(http.HandlerFunc(adminHandler.KYCApplication)))
		mux.Handle("POST /v1/admin/kyc/{id}/approve", requireSession(authService)(http.HandlerFunc(adminHandler.KYCReview)))
		mux.Handle("POST /v1/admin/kyc/{id}/reject", requireSession(authService)(http.HandlerFunc(adminHandler.KYCReview)))
		mux.Handle("GET /v1/admin/operational-controls/conversions", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionControl)))
		mux.Handle("PUT /v1/admin/operational-controls/conversions", requireSession(authService)(http.HandlerFunc(adminHandler.ConversionControl)))
		assetHandler := NewAssetHandler(assets.NewService(data), logger)
		mux.HandleFunc("GET /v1/assets/catalog", assetHandler.Catalog)
	}
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, _ *http.Request) {
		writeVersionedError(w, http.StatusNotFound, "ROUTE_NOT_FOUND", "API route was not found")
	})

	handler := recoverer(logger)(requestID(logger)(metrics.Middleware(securityHeaders(customerCORS(cfg.AllowedBrowserOrigins, csrfOriginCheck(cfg.AllowedBrowserOrigins, mux))))))
	server := &http.Server{
		Addr:              cfg.HTTPAddress,
		Handler:           handler,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    16 << 10,
	}
	server.RegisterOnShutdown(func() {
		stopBackground()
		for _, closeResource := range shutdownClosers {
			closeResource()
		}
	})
	return server
}
