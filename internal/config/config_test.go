package config

import (
	"testing"
	"time"
)

func TestDatabaseURLFromRDSSecretFields(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RDS_DB_HOST", "limiance.example.rds.amazonaws.com")
	t.Setenv("RDS_DB_PORT", "5432")
	t.Setenv("RDS_DB_USERNAME", "limiance")
	t.Setenv("RDS_DB_PASSWORD", "p@ss:/word")
	t.Setenv("RDS_DB_NAME", "limiance")

	got := databaseURL()
	want := "postgres://limiance:p%40ss%3A%2Fword@limiance.example.rds.amazonaws.com:5432/limiance?sslmode=require"
	if got != want {
		t.Fatalf("databaseURL() = %q, want %q", got, want)
	}
}

func TestDatabaseURLTakesPrecedence(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://local")
	t.Setenv("RDS_DB_HOST", "ignored.example")
	t.Setenv("RDS_DB_USERNAME", "ignored")
	t.Setenv("RDS_DB_PASSWORD", "ignored")

	if got := databaseURL(); got != "postgres://local" {
		t.Fatalf("databaseURL() = %q", got)
	}
}

func TestDatabaseURLFromRDSSecretJSON(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("RDS_DB_HOST", "limiance.example.rds.amazonaws.com")
	t.Setenv("RDS_DB_PORT", "5432")
	t.Setenv("RDS_DB_NAME", "limiance")
	t.Setenv("RDS_DB_USERNAME", "")
	t.Setenv("RDS_DB_PASSWORD", "")
	t.Setenv("RDS_SECRET_JSON", `{"username":"limiance","password":"p@ss:/word"}`)

	got := databaseURL()
	want := "postgres://limiance:p%40ss%3A%2Fword@limiance.example.rds.amazonaws.com:5432/limiance?sslmode=require"
	if got != want {
		t.Fatalf("databaseURL() = %q, want %q", got, want)
	}
}

func TestMatchingEngineTransportConfiguration(t *testing.T) {
	t.Setenv("MATCHING_ENGINE_ORDER_ENDPOINT", "tcp://engine:6001")
	t.Setenv("MATCHING_ENGINE_CONTROL_ENDPOINT", "tcp://engine:6002")
	t.Setenv("MATCHING_ENGINE_EVENT_ENDPOINT", "tcp://engine:6003")
	t.Setenv("MATCHING_ENGINE_REQUEST_TIMEOUT", "750ms")

	config := Load()
	if config.MatchingEngineOrderURL != "tcp://engine:6001" || config.MatchingEngineControlURL != "tcp://engine:6002" {
		t.Fatalf("unexpected matching engine endpoints: order=%q control=%q", config.MatchingEngineOrderURL, config.MatchingEngineControlURL)
	}
	if config.MatchingEngineEventsURL != "tcp://engine:6003" {
		t.Fatalf("unexpected matching engine event endpoint: %q", config.MatchingEngineEventsURL)
	}
	if config.MatchingEngineTimeout != 750*time.Millisecond {
		t.Fatalf("unexpected matching engine timeout: %s", config.MatchingEngineTimeout)
	}
}
