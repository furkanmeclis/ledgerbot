package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/migrations"
)

// Integration test: set LEDGERBOT_TEST_DATABASE_URL to an empty database
// (CI uses a pgvector service container).
func testPG(t *testing.T) *PG {
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
	sqlDB := stdlib.OpenDBFromPool(db)
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	_ = goose.SetDialect("postgres")
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		t.Fatal(err)
	}
	return &PG{DB: db}
}

func TestPGDraftLifecycle(t *testing.T) {
	p := testPG(t)
	ctx := context.Background()
	c := domain.CompanyID("pgtest-" + time.Now().Format("150405.000000"))
	d := domain.Draft{ID: "d-" + string(c), CompanyID: c, ActorID: "u1", Capability: "record_collection", Status: domain.DraftPending,
		Payload: json.RawMessage(`{"amount":100}`), Summary: "s", Channel: domain.ChannelDev, Sender: "+905320000009"}
	if err := p.CreateDraft(ctx, &d); err != nil {
		t.Fatal(err)
	}
	got, err := p.LatestPending(ctx, c, domain.ChannelDev, d.Sender, time.Hour)
	if err != nil || got.ID != d.ID {
		t.Fatalf("latest pending: %+v %v", got, err)
	}
	if _, err := p.TransitionDraft(ctx, c, d.ID, domain.DraftPending, domain.DraftCommitted, "EXT-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.TransitionDraft(ctx, c, d.ID, domain.DraftPending, domain.DraftCommitted, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("double commit must conflict, got %v", err)
	}
	if _, dup, _ := p.ClaimFingerprint(ctx, c, "fp", d.ID); dup {
		t.Fatal("first claim is not a duplicate")
	}
	if prev, dup, _ := p.ClaimFingerprint(ctx, c, "fp", "other"); !dup || prev != d.ID {
		t.Fatalf("second claim: %v %s", dup, prev)
	}
	if ok, _ := p.ThrottleOnce(ctx, c, "k", time.Hour); !ok {
		t.Fatal("first throttle")
	}
	if ok, _ := p.ThrottleOnce(ctx, c, "k", time.Hour); ok {
		t.Fatal("second throttle should be suppressed")
	}
	_ = p.Audit(ctx, AuditEntry{CompanyID: c, Action: "message.received", Detail: json.RawMessage(`{"sender":"+905320000009"}`)})
	n, err := p.EraseSubject(ctx, DeletionRequest{CompanyID: c, Subject: "+905320000009"})
	if err != nil || n != 2 {
		t.Fatalf("erase: %d %v", n, err)
	}
	if _, err := p.GetDraft(ctx, c, d.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("draft should be gone: %v", err)
	}
}
