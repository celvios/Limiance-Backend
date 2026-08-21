package config

import (
	"bufio"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment               string
	HTTPAddress               string
	DatabaseURL               string
	SumsubWebhookKey          string
	SumsubAppToken            string
	SumsubSecretKey           string
	SumsubLevelName           string
	FireblocksWebhook         string
	FireblocksAPIKey          string
	FireblocksPrivateKey      string
	FireblocksBaseURL         string
	FireblocksJWKSURL         string
	CustodyMode               string
	SelfCustodyTestnetEnabled bool
	SelfCustodySignerURL      string
	SelfCustodyKMSKeyID       string
	SendGridAPIKey            string
	SendGridFromEmail         string
	SendGridTemplate          string
	TwilioAccountSID          string
	TwilioAPIKey              string
	TwilioAPISecret           string
	TwilioVerifySID           string
	VerificationPepper        string
	VerificationEncryptionKey string
	TravelRuleEncryptionKey   string
	TOTPEncryptionKey         string
	BybitMarketDataBaseURL    string
	AWSRegion                 string
	SQSOutboxQueueURL         string
	SQSNotificationsQueueURL  string
	SQSDepositsQueueURL       string
	SQSUnroutedQueueURL       string
	SQSEndpoint               string
	SessionTTL                time.Duration
	WithdrawalAddressCooldown time.Duration
	RequestBodyLimit          int64
	ReadTimeout               time.Duration
	ReadHeaderTimeout         time.Duration
	WriteTimeout              time.Duration
	IdleTimeout               time.Duration
	LogLevel                  slog.Level
}

func Load() Config {
	loadDotEnv()
	return Config{
		Environment:       value("APP_ENV", "development"),
		HTTPAddress:       value("HTTP_ADDRESS", ":8080"),
		DatabaseURL:       databaseURL(),
		SumsubWebhookKey:  strings.TrimSpace(os.Getenv("SUMSUB_WEBHOOK_SECRET")),
		SumsubAppToken:    strings.TrimSpace(os.Getenv("SUMSUB_APP_TOKEN")),
		SumsubSecretKey:   strings.TrimSpace(os.Getenv("SUMSUB_SECRET_KEY")),
		SumsubLevelName:   value("SUMSUB_LEVEL_NAME", ""),
		FireblocksWebhook: strings.TrimSpace(os.Getenv("FIREBLOCKS_WEBHOOK_SECRET")),
		FireblocksAPIKey:  strings.TrimSpace(os.Getenv("FIREBLOCKS_API_KEY")),
		// .env values are single-line. Permit a PEM represented with literal
		// "\\n" separators, while still accepting a normal multiline PEM injected
		// by a production secret manager.
		FireblocksPrivateKey:      strings.ReplaceAll(strings.TrimSpace(os.Getenv("FIREBLOCKS_PRIVATE_KEY")), "\\n", "\n"),
		FireblocksBaseURL:         strings.TrimSpace(os.Getenv("FIREBLOCKS_BASE_URL")),
		FireblocksJWKSURL:         value("FIREBLOCKS_JWKS_URL", "https://eu-keys.fireblocks.io/.well-known/jwks.json"),
		CustodyMode:               value("CUSTODY_MODE", "fireblocks"),
		SelfCustodyTestnetEnabled: boolValue("SELF_CUSTODY_TESTNET_ENABLED", false),
		SelfCustodySignerURL:      strings.TrimSpace(os.Getenv("SELF_CUSTODY_SIGNER_URL")),
		SelfCustodyKMSKeyID:       strings.TrimSpace(os.Getenv("SELF_CUSTODY_KMS_KEY_ID")),
		SendGridAPIKey:            strings.TrimSpace(os.Getenv("SENDGRID_API_KEY")),
		SendGridFromEmail:         strings.TrimSpace(os.Getenv("SENDGRID_FROM_EMAIL")),
		SendGridTemplate:          strings.TrimSpace(os.Getenv("SENDGRID_VERIFICATION_TEMPLATE_ID")),
		TwilioAccountSID:          strings.TrimSpace(os.Getenv("TWILIO_ACCOUNT_SID")),
		TwilioAPIKey:              strings.TrimSpace(os.Getenv("TWILIO_API_KEY")),
		TwilioAPISecret:           strings.TrimSpace(os.Getenv("TWILIO_API_SECRET")),
		TwilioVerifySID:           strings.TrimSpace(os.Getenv("TWILIO_VERIFY_SERVICE_SID")),
		VerificationPepper:        strings.TrimSpace(os.Getenv("VERIFICATION_CODE_PEPPER")),
		VerificationEncryptionKey: strings.TrimSpace(os.Getenv("VERIFICATION_ENCRYPTION_KEY")),
		TravelRuleEncryptionKey:   strings.TrimSpace(os.Getenv("TRAVEL_RULE_ENCRYPTION_KEY")),
		TOTPEncryptionKey:         strings.TrimSpace(os.Getenv("TOTP_ENCRYPTION_KEY")),
		BybitMarketDataBaseURL:    value("BYBIT_MARKET_DATA_BASE_URL", "https://api.bybit.com"),
		AWSRegion:                 value("AWS_REGION", "eu-central-1"),
		SQSOutboxQueueURL:         strings.TrimSpace(os.Getenv("SQS_OUTBOX_QUEUE_URL")),
		SQSNotificationsQueueURL:  strings.TrimSpace(os.Getenv("SQS_NOTIFICATIONS_QUEUE_URL")),
		SQSDepositsQueueURL:       strings.TrimSpace(os.Getenv("SQS_DEPOSITS_QUEUE_URL")),
		SQSUnroutedQueueURL:       strings.TrimSpace(os.Getenv("SQS_UNROUTED_QUEUE_URL")),
		SQSEndpoint:               strings.TrimSpace(os.Getenv("SQS_ENDPOINT")),
		SessionTTL:                durationValue("SESSION_TTL", 24*time.Hour),
		WithdrawalAddressCooldown: durationValue("WITHDRAWAL_ADDRESS_COOLDOWN", 24*time.Hour),
		RequestBodyLimit:          intValue("HTTP_MAX_BODY_BYTES", 1<<20),
		ReadTimeout:               durationValue("HTTP_READ_TIMEOUT", 10*time.Second),
		ReadHeaderTimeout:         durationValue("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		WriteTimeout:              durationValue("HTTP_WRITE_TIMEOUT", 20*time.Second),
		IdleTimeout:               durationValue("HTTP_IDLE_TIMEOUT", 60*time.Second),
		LogLevel:                  logLevel(value("LOG_LEVEL", "info")),
	}
}

// databaseURL supports the legacy DATABASE_URL setting and the individual
// fields stored by the AWS-managed RDS secret. DATABASE_URL wins so existing
// local development environments remain unchanged. ECS should inject the RDS
// secret JSON keys as RDS_DB_HOST, RDS_DB_PORT, RDS_DB_USERNAME, and
// RDS_DB_PASSWORD, plus RDS_DB_NAME=limiance. Building the URL here avoids
// copying a generated RDS password into a second secret or mishandling URL
// reserved characters in that password.
func databaseURL() string {
	if raw := strings.TrimSpace(os.Getenv("DATABASE_URL")); raw != "" {
		return raw
	}
	host := strings.TrimSpace(os.Getenv("RDS_DB_HOST"))
	username := strings.TrimSpace(os.Getenv("RDS_DB_USERNAME"))
	password := os.Getenv("RDS_DB_PASSWORD")
	if host == "" || username == "" || password == "" {
		return ""
	}
	port := value("RDS_DB_PORT", "5432")
	database := value("RDS_DB_NAME", "limiance")
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(host, port),
		Path:     database,
		RawQuery: "sslmode=require",
	}).String()
}

// loadDotEnv is a development convenience. Explicit process environment
// variables always win, which keeps container and production configuration in
// the platform secret store rather than in a file.
func loadDotEnv() {
	directory, err := os.Getwd()
	if err != nil {
		return
	}
	var file *os.File
	for {
		file, err = os.Open(filepath.Join(directory, ".env"))
		if err == nil {
			break
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return
		}
		directory = parent
	}
	defer file.Close()
	for scanner := bufio.NewScanner(file); scanner.Scan(); {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, raw, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" || os.Getenv(key) != "" {
			continue
		}
		value := strings.Trim(strings.TrimSpace(raw), "\"'")
		_ = os.Setenv(key, value)
	}
}

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func intValue(key string, fallback int64) int64 {
	v, err := strconv.ParseInt(value(key, strconv.FormatInt(fallback, 10)), 10, 64)
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func boolValue(key string, fallback bool) bool {
	v, err := strconv.ParseBool(value(key, strconv.FormatBool(fallback)))
	if err != nil {
		return fallback
	}
	return v
}

func durationValue(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(value(key, fallback.String()))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func logLevel(raw string) slog.Level {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
