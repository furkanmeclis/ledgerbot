package rag

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
)

func TestSplit(t *testing.T) {
	long := strings.Repeat("Bu bir cümle. ", 200)
	parts := Split(long, 300)
	if len(parts) < 5 {
		t.Fatalf("expected several chunks, got %d", len(parts))
	}
	for _, p := range parts {
		if len([]rune(p)) > 300 {
			t.Fatalf("chunk too long: %d", len([]rune(p)))
		}
	}
	if Split("   ", 100) != nil {
		t.Fatal("blank text must give no chunks")
	}
}

func TestLiteral(t *testing.T) {
	if got := Literal([]float32{0.5, -1, 0}); got != "[0.5,-1,0]" {
		t.Fatalf("literal %s", got)
	}
}

func TestPGSearch(t *testing.T) {
	ctx := context.Background()
	db := testdb.Open(t)
	idx := &PG{DB: db, Embedder: ai.FakeEmbedder{Dims: 64}}
	a := domain.CompanyID("rag-a-" + time.Now().Format("150405.000000"))
	b := domain.CompanyID("rag-b-" + time.Now().Format("150405.000000"))
	for _, txt := range []string{"depo kirası her ay Demir Yapıya ödenir", "toner siparişleri Örnek Kırtasiyeden verilir", "personel maaşları ayın 1inde"} {
		if err := idx.Add(ctx, a, "note", "", txt); err != nil {
			t.Fatal(err)
		}
	}
	_ = idx.Add(ctx, b, "note", "", "depo kirası başka firmanın notu")
	hits, err := idx.Search(ctx, a, "depo kirası", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.Contains(hits[0].Content, "Demir") {
		t.Fatalf("best hit wrong: %+v", hits)
	}
	for _, h := range hits {
		if strings.Contains(h.Content, "başka firmanın") {
			t.Fatal("cross-company hit")
		}
	}
}
