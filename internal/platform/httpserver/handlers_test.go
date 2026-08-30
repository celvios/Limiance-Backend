package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONStandardizesLegacyErrors(t *testing.T) {
	response := httptest.NewRecorder()
	writeJSON(response, http.StatusServiceUnavailable, map[string]string{"error": "balances_unavailable"})
	var body errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "BALANCES_UNAVAILABLE" || body.Error.Message != "balances unavailable" || body.Error.RequestID == "" {
		t.Fatalf("unexpected standardized error: %+v", body)
	}
}
