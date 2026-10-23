// Package testutil starts a real Postgres for tests. It uses an embedded
// Postgres binary rather than Docker so `go test ./...` works on any machine
// and in CI. One database per test binary; tables are truncated between
// tests rather than the server restarted.
package testutil

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/migrations"
)

// DB is a started database with its pool.
type DB struct {
	Pool *pgxpool.Pool
	DSN  string
	pg   *embeddedpostgres.EmbeddedPostgres
}

// Start boots Postgres, runs migrations and returns a pool. Call Stop when
// the package's tests finish. If TEST_DATABASE_URL is set it is used instead,
// which is faster for repeated local runs.
func Start() (*DB, error) {
	ctx := context.Background()
	if dsn := os.Getenv("TEST_DATABASE_URL"); dsn != "" {
		if err := migrations.Up(dsn); err != nil {
			return nil, err
		}
		pool, err := pgxpool.New(ctx, dsn)
		if err != nil {
			return nil, err
		}
		return &DB{Pool: pool, DSN: dsn}, nil
	}

	port, err := freePort()
	if err != nil {
		return nil, err
	}
	runtime, err := os.MkdirTemp("", "wallet-pg-")
	if err != nil {
		return nil, err
	}
	cache := filepath.Join(os.TempDir(), "embedded-postgres-cache")
	cfg := embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		Username("test").Password("test").Database("wallet_test").
		RuntimePath(runtime).CachePath(cache).
		Logger(nil)
	pg := embeddedpostgres.NewDatabase(cfg)
	if err := pg.Start(); err != nil {
		return nil, fmt.Errorf("embedded postgres: %w", err)
	}
	dsn := fmt.Sprintf("postgres://test:test@localhost:%d/wallet_test?sslmode=disable", port)
	if err := migrations.Up(dsn); err != nil {
		_ = pg.Stop()
		return nil, err
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		_ = pg.Stop()
		return nil, err
	}
	return &DB{Pool: pool, DSN: dsn, pg: pg}, nil
}

// Stop closes the pool and the server.
func (d *DB) Stop() {
	d.Pool.Close()
	if d.pg != nil {
		_ = d.pg.Stop()
	}
}

// Truncate empties every application table between tests.
func (d *DB) Truncate(t *testing.T) {
	t.Helper()
	_, err := d.Pool.Exec(context.Background(),
		`TRUNCATE telemetry_events, idempotency_keys, transfers, ledger_entries, wallet_balances, wallets, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port, nil
}
