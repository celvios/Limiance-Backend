package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
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
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
