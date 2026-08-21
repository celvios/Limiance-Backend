package config

import "testing"

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
