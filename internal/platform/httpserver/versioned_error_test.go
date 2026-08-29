package httpserver

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestVersionedErrorAlwaysIncludesRequestID(t *testing.T) {
	response := httptest.NewRecorder()
	writeVersionedError(response, 400, "INVALID_REQUEST", "request is invalid")
	var body errorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "INVALID_REQUEST" || body.Error.Message == "" || body.Error.RequestID == "" {
		t.Fatalf("incomplete error envelope: %+v", body)
	}
}
