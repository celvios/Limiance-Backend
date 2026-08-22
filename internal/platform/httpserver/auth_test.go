package httpserver

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/limiance/backend/internal/auth"
)

func TestProductionSessionCookieSupportsCredentialedCrossSiteFrontend(t *testing.T) {
	handler := NewAuthHandler(nil, nil, true)
	response := httptest.NewRecorder()
	handler.setSessionCookie(response, auth.LoginResult{Token: "session-token", ExpiresAt: time.Now().Add(time.Hour)})

	cookie := response.Header().Get("Set-Cookie")
	if !strings.Contains(cookie, "Secure") || !strings.Contains(cookie, "SameSite=None") || !strings.Contains(cookie, "HttpOnly") {
		t.Fatalf("session cookie lacks required cross-site protections: %q", cookie)
	}
}
