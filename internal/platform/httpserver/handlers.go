package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type pinger interface{ Ping(context.Context) error }

func ready(pool pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database_unconfigured"})
			return
		}
		if err := pool.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database_unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}

func apiIndex(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "limiance-api", "version": "v1"})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	// Convert legacy v1 error maps at the response boundary so every endpoint
	// emits the documented envelope without invasive changes to money-moving
	// handlers. Successful response contracts remain untouched.
	if status >= http.StatusBadRequest {
		if legacy, ok := payload.(map[string]string); ok {
			if rawCode, exists := legacy["error"]; exists && rawCode != "" {
				requestID := w.Header().Get("X-Request-ID")
				if requestID == "" {
					requestID = newRequestID()
					w.Header().Set("X-Request-ID", requestID)
				}
				details := make(map[string]any)
				for key, value := range legacy {
					if key != "error" {
						details[key] = value
					}
				}
				if len(details) == 0 {
					details = nil
				}
				payload = errorEnvelope{Error: errorDetail{
					Code:      strings.ToUpper(rawCode),
					Message:   strings.ReplaceAll(rawCode, "_", " "),
					Details:   details,
					RequestID: requestID,
				}}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
