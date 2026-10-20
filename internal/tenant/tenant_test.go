package tenant

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

type flakySource struct {
	fail  atomic.Bool
	calls atomic.Int32
}

func (f *flakySource) Settings(context.Context, domain.CompanyID) (Settings, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return Settings{}, errors.New("down")
	}
	return Settings{SaveMode: domain.SaveAuto}, nil
}

func (f *flakySource) Matrix(context.Context, domain.CompanyID) (Matrix, error) {
	return Matrix{Roles: map[string]Role{"acc": {Capabilities: []string{"query_*", "record_collection"}, SaveMode: domain.SaveAskApproval}}}, nil
}

func TestCacheDegradesSafely(t *testing.T) {
	src := &flakySource{}
	c := NewCache(src, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	snap, err := c.Get(context.Background(), "x")
	if err != nil || snap.Degraded {
		t.Fatalf("first get: %v %v", snap, err)
	}
	src.fail.Store(true)
	c.Invalidate("x")
	if _, err := c.Get(context.Background(), "x"); err == nil {
		t.Fatal("no snapshot and source down must error")
	}
	src.fail.Store(false)
	if _, err := c.Get(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	src.fail.Store(true)
	time.Sleep(2 * time.Millisecond)
	d, err := c.refresh(context.Background(), "x")
	if err != nil || !d.Degraded || d.EffectiveSaveMode("acc") != domain.SaveReplyOnly {
		t.Fatalf("degraded snapshot must disable writes: %+v %v", d, err)
	}
}

func TestPermissions(t *testing.T) {
	s := NewSnapshot(Settings{SaveMode: domain.SaveAuto}, Matrix{
		Roles: map[string]Role{
			"acc":   {Capabilities: []string{"query_*", "record_collection"}, SaveMode: domain.SaveAskApproval},
			"sales": {Capabilities: []string{"query_*"}, Channels: []domain.ChannelKind{domain.ChannelTelegram}},
		},
		Users: []User{{ID: "u1", Role: "acc", Addresses: map[domain.ChannelKind]string{domain.ChannelWuzapi: "+905320000001"}}},
	})
	if !s.Allowed("acc", "query_balance", domain.ChannelDev) || s.Allowed("acc", "record_payment", domain.ChannelDev) {
		t.Fatal("capability globs")
	}
	if s.Allowed("sales", "query_stock", domain.ChannelWuzapi) || !s.Allowed("sales", "query_stock", domain.ChannelTelegram) {
		t.Fatal("channel restriction")
	}
	if s.EffectiveSaveMode("acc") != domain.SaveAskApproval {
		t.Fatal("role must tighten save mode")
	}
	// WABA and WuzAPI share phone numbers.
	if a, ok := s.ResolveActor(domain.ChannelWABA, "+905320000001"); !ok || a.UserID != "u1" {
		t.Fatal("whatsapp address lookup")
	}
	if a, ok := s.ResolveActor(domain.ChannelAPI, "user:u1"); !ok || a.Role != "acc" {
		t.Fatal("api actor lookup")
	}
}
