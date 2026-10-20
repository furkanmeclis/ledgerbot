package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
)

// Integration test: set LEDGERBOT_TEST_DATABASE_URL to a PostgreSQL database
// with pgvector (CI starts one).
func testPG(t *testing.T) *PG {
	t.Helper()
	return &PG{DB: testdb.Open(t)}
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
