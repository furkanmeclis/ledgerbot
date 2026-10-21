// Package testdb connects integration tests to PostgreSQL and applies
// migrations once, serialized with an advisory lock so packages tested in
// parallel do not race on DDL.
package testdb

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/furkanmeclis/ledgerbot/migrations"
)

// Open returns a migrated pool, or skips when LEDGERBOT_TEST_DATABASE_URL is unset.
func Open(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("LEDGERBOT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("LEDGERBOT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(7347001)`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `SELECT pg_advisory_unlock(7347001)`) //nolint:errcheck
	sqlDB := stdlib.OpenDBFromPool(db)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		t.Fatal(err)
	}
	return db
}
