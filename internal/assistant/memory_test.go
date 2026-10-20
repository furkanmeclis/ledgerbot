package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/rag"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func withKnowledge(h *harness) *rag.Mem {
	idx := &rag.Mem{Embedder: ai.FakeEmbedder{Dims: 64}}
	h.e.Knowledge = &Knowledge{Index: idx, MinScore: 0.2, Remember: func(ctx context.Context, c domain.CompanyID, source, ref, text string) error {
		return idx.Add(ctx, c, source, ref, text)
	}}
	return idx
}

func TestRepeatLast(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	withKnowledge(h)
	if r := h.send(t, "owner", "her zamanki gibi"); !strings.Contains(joined(r), "önceki bir kaydınız yok") {
		t.Fatalf("no history: %q", joined(r))
	}
	h.send(t, "owner", "Mehmet'e 2.000 TL ödeme yaptım")
	r := h.send(t, "owner", "her zamanki gibi 750 TL")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted || !strings.Contains(joined(r), "750,00 TL") || !strings.Contains(joined(r), "Her zamanki gibi") {
		t.Fatalf("repeat with new amount: %+v %q", r.Drafts, joined(r))
	}
	r = h.send(t, "owner", "her zamanki gibi")
	if len(r.Drafts) != 1 || !strings.Contains(joined(r), "750,00 TL") {
		t.Fatalf("repeat repeats the latest: %q", joined(r))
	}
}

func TestNotesAndKnowledge(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	withKnowledge(h)
	if r := h.send(t, "owner", "not al: depo kirası her ayın 5'inde Demir Yapı'ya ödenir"); !strings.Contains(joined(r), "Not aldım") {
		t.Fatalf("add note: %q", joined(r))
	}
	h.send(t, "owner", "Ahmet Yılmaz'dan 3.000 TL tahsilat")
	r := h.send(t, "owner", "notlarda depo kirası ara")
	if !strings.Contains(joined(r), "Demir Yapı") || !strings.Contains(joined(r), "[not]") {
		t.Fatalf("note search: %q", joined(r))
	}
	r = h.send(t, "owner", "kayıtlarda Ahmet Yılmaz tahsilat ara")
	if !strings.Contains(joined(r), "[kayıt]") || !strings.Contains(joined(r), "3.000,00 TL") {
		t.Fatalf("record search: %q", joined(r))
	}
	// Tenant isolation: another company sees nothing.
	idx := h.e.Knowledge.Index
	hits, _ := idx.Search(context.Background(), "other", "depo kirası", 5)
	if len(hits) != 0 {
		t.Fatalf("knowledge leaked across companies: %+v", hits)
	}
}

func TestKnowledgeDisabled(t *testing.T) {
	h := newHarness(t, nil)
	if r := h.send(t, "owner", "not al: deneme"); !strings.Contains(joined(r), "kapalı") {
		t.Fatalf("disabled memory: %q", joined(r))
	}
}
