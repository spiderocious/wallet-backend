package app_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/migrations"
)

// The release step must bootstrap its own database on a fresh instance and
// be safe to run again once it exists.
func TestMigrateCreatesMissingDatabase(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") != "" {
		t.Skip("needs a server we may create databases on")
	}
	cfg, err := pgx.ParseConfig(tdb.DSN)
	if err != nil {
		t.Fatal(err)
	}
	fresh := cfg.Copy()
	fresh.Database = "bootstrap_check"
	dsn := strings.Replace(tdb.DSN, "/"+cfg.Database+"?", "/"+fresh.Database+"?", 1)
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := migrations.Up(dsn); err != nil {
		t.Fatalf("second run: %v", err)
	}
	conn, err := pgx.ConnectConfig(context.Background(), fresh)
	if err != nil {
		t.Fatalf("connect to created db: %v", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var n int
	if err := conn.QueryRow(context.Background(), "SELECT count(*) FROM goose_db_version").Scan(&n); err != nil || n == 0 {
		t.Fatalf("migrations not applied: n=%d err=%v", n, err)
	}
}
