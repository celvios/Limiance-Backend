package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/limiance/backend/internal/datamanager"
	"golang.org/x/oauth2"
)

var ErrExternalAuthentication = errors.New("external authentication failed")

type ExternalClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
}

type OIDCProvider struct {
	Name     string
	Redirect string
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
}

type OIDCProviderConfig struct {
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

func NewOIDCProvider(ctx context.Context, cfg OIDCProviderConfig) (*OIDCProvider, error) {
	if cfg.Name == "" || cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURL == "" {
		return nil, ErrExternalAuthentication
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	return &OIDCProvider{
		Name:     cfg.Name,
		Redirect: cfg.RedirectURL,
		config: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "email", "profile"},
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
	}, nil
}

func (p *OIDCProvider) AuthURL(state, verifier string) string {
	return p.config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.S256ChallengeOption(verifier))
}

func (p *OIDCProvider) Claims(ctx context.Context, code, state, verifier string) (ExternalClaims, error) {
	token, err := p.config.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return ExternalClaims{}, ErrExternalAuthentication
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return ExternalClaims{}, ErrExternalAuthentication
	}
	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return ExternalClaims{}, ErrExternalAuthentication
	}
	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Nonce         string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Subject == "" || claims.Nonce != state {
		return ExternalClaims{}, ErrExternalAuthentication
	}
	if claims.Email != "" {
		parsed, err := mail.ParseAddress(claims.Email)
		if err != nil || parsed.Address != claims.Email || strings.ContainsAny(claims.Email, "\r\n") {
			return ExternalClaims{}, ErrExternalAuthentication
		}
	}
	return ExternalClaims{Subject: claims.Subject, Email: strings.ToLower(strings.TrimSpace(claims.Email)), EmailVerified: claims.EmailVerified}, nil
}

func (s *Service) LoginExternal(ctx context.Context, provider string, claims ExternalClaims, meta datamanager.SessionMetadata) (LoginResult, error) {
	if !claims.EmailVerified || claims.Subject == "" {
		return LoginResult{}, ErrExternalAuthentication
	}
	user, err := s.data.ResolveExternalIdentity(ctx, provider, claims.Subject, claims.Email)
	if err != nil {
		return LoginResult{}, err
	}
	if user.Status == "frozen" {
		return LoginResult{}, ErrAccountFrozen
	}
	if user.Status != "active" {
		return LoginResult{}, ErrVerificationNeeded
	}
	return s.createSession(ctx, user.ID, provider, meta)
}
