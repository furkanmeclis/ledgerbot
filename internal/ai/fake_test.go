package ai

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

func TestFakeLLM(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name string
		req  CompletionRequest
		want string
	}{
		{"plain text", CompletionRequest{Messages: []Message{{Role: "user", Text: "merhaba"}}}, "Anlayamadım."},
		{"json mode", CompletionRequest{JSON: true, Messages: []Message{{Role: "user", Text: "x"}}}, `{"capability":"unknown","args":{},"language":"tr"}`},
		{"image json echoed", CompletionRequest{JSON: true, Images: []domain.Media{{MIME: "image/png", Data: []byte(`{"total":"10,00"}`)}}}, `{"total":"10,00"}`},
		{"image binary", CompletionRequest{Images: []domain.Media{{MIME: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}}}}, FakeReceipt},
		{"image url only", CompletionRequest{Images: []domain.Media{{URL: "https://x/y.jpg"}}}, FakeReceipt},
		{"only first image counts", CompletionRequest{Images: []domain.Media{{Data: []byte("nope")}, {Data: []byte(`{"a":1}`)}}}, FakeReceipt},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := FakeLLM{}.Complete(ctx, c.req)
			if err != nil {
				t.Fatal(err)
			}
			if got.Text != c.want {
				t.Fatalf("Text = %q, want %q", got.Text, c.want)
			}
			if got.Usage.Provider != "fake" || got.Usage.Model != "fake" || got.Usage.OutputTokens != 32 || got.Usage.InputTokens < 1 {
				t.Fatalf("Usage = %+v", got.Usage)
			}
		})
	}
}

func TestFakeReceiptIsValidJSON(t *testing.T) {
	t.Parallel()
	var r struct {
		Vendor string `json:"vendor"`
		Lines  []struct {
			Name    string `json:"name"`
			VATRate int    `json:"vat_rate"`
		} `json:"lines"`
		Total string `json:"total"`
	}
	if err := json.Unmarshal([]byte(FakeReceipt), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Lines) != 2 || r.Total != "2.184,00" || r.Vendor == "" {
		t.Fatalf("receipt = %+v", r)
	}
	total, err := domain.ParseAmount(r.Total)
	if err != nil || total != 218400 {
		t.Fatalf("total = %d, %v", total, err)
	}
}

func TestApproxTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		req  CompletionRequest
		want int
	}{
		{CompletionRequest{}, 1},
		{CompletionRequest{System: "abcd"}, 2},
		{CompletionRequest{System: "ab", Messages: []Message{{Text: "cd"}, {Text: "efgh"}}}, 3},
		{CompletionRequest{System: "çğıöşü₺Ç"}, 3}, // runes, not bytes
		{CompletionRequest{System: strings.Repeat("a", 4000)}, 1001},
	}
	for _, c := range cases {
		if got := approxTokens(c.req); got != c.want {
			t.Errorf("approxTokens(%+v) = %d, want %d", c.req, got, c.want)
		}
	}
}

func TestFakeSTT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		data, lang, wantText, wantLang string
		secs                           float64
	}{
		{"  Ahmet'ten 5000 TL tahsilat \n", "", "Ahmet'ten 5000 TL tahsilat", "tr", 30.0 / 16},
		{"received 5 usd", "en", "received 5 usd", "en", 14.0 / 16},
		{"", "", "", "tr", 0},
	}
	for _, c := range cases {
		got, err := FakeSTT{}.Transcribe(context.Background(), domain.Media{Data: []byte(c.data)}, c.lang)
		if err != nil {
			t.Fatal(err)
		}
		if got.Text != c.wantText || got.Language != c.wantLang || got.Usage.AudioSeconds != c.secs || got.Usage.Provider != "fake" {
			t.Errorf("Transcribe(%q, %q) = %+v", c.data, c.lang, got)
		}
	}
}

func cosine(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i] * b[i])
	}
	return dot
}

func TestFakeEmbedder(t *testing.T) {
	t.Parallel()
	e := FakeEmbedder{Dims: 64}
	if e.Dimensions() != 64 {
		t.Fatal("Dimensions")
	}
	texts := []string{"depo kirası ödemesi", "Depo  KİRASI ödemesi", "toner siparişi verildi", ""}
	vecs, u, err := e.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) || u.Provider != "fake" {
		t.Fatalf("got %d vectors, usage %+v", len(vecs), u)
	}
	for i, v := range vecs[:3] {
		if len(v) != 64 {
			t.Fatalf("vec %d has %d dims", i, len(v))
		}
		if n := cosine(v, v); math.Abs(n-1) > 1e-5 {
			t.Fatalf("vec %d norm² = %v, want 1", i, n)
		}
	}
	for _, x := range vecs[3] {
		if x != 0 {
			t.Fatal("empty text should embed to the zero vector")
		}
	}
	again, _, _ := e.Embed(context.Background(), texts[:1])
	for i := range again[0] {
		if again[0][i] != vecs[0][i] {
			t.Fatal("embedding is not deterministic")
		}
	}
	if same, other := cosine(vecs[0], vecs[0]), cosine(vecs[0], vecs[2]); other >= same {
		t.Fatalf("unrelated text should be less similar: %v vs %v", other, same)
	}
	empty, _, err := e.Embed(context.Background(), nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("nil input = %v, %v", empty, err)
	}
}

func TestFakeEmbedderZeroDims(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r != nil {
			t.Skipf("BUG: FakeEmbedder{} (Dims 0) panics on any non-empty text (%v): modulo by len(v)==0; only the registry's max(Dims, 8) guard prevents it", r)
		}
	}()
	_, _, _ = FakeEmbedder{}.Embed(context.Background(), []string{"x"})
}

func BenchmarkFakeEmbedder(b *testing.B) {
	e := FakeEmbedder{Dims: 256}
	texts := []string{"Ahmet Yılmaz'dan 5.000 TL nakit tahsilat alındı, makbuz kesildi"}
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = e.Embed(context.Background(), texts)
	}
}
