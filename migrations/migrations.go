// Package migrations embeds the SQL files and runs them with goose. Migrations
// are forward-only and run as a release step (cmd/migrate), never on boot.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// Up applies every pending migration to the database at dsn, creating the
// database first when it does not exist.
func Up(dsn string) error {
	if err := EnsureDatabase(context.Background(), dsn); err != nil {
		return err
	}
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("migrate open: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	goose.SetBaseFS(files)
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// EnsureDatabase creates the database named in dsn when it does not exist.
// A hosted Postgres instance comes with one database; both demo apps share
// an instance, so the release step makes its own database on first run
// instead of asking an operator to.
func EnsureDatabase(ctx context.Context, dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("migrate parse dsn: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err == nil {
		return conn.Close(ctx)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "3D000" { // invalid_catalog_name
		return fmt.Errorf("migrate connect: %w", err)
	}
	admin := cfg.Copy()
	admin.Database = "postgres"
	ac, err := pgx.ConnectConfig(ctx, admin)
	if err != nil {
		return fmt.Errorf("migrate connect to postgres db: %w", err)
	}
	defer func() { _ = ac.Close(ctx) }()
	_, err = ac.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{cfg.Database}.Sanitize())
	if errors.As(err, &pgErr) && pgErr.Code == "42P04" { // duplicate_database: another replica won the race
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate create database %q: %w", cfg.Database, err)
	}
	return nil
}
