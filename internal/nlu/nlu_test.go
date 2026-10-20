package nlu

import (
	"context"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
)

func TestParseRules(t *testing.T) {
	cases := []struct {
		in   string
		caps []string
		args map[string]string // checked on the first intent
	}{
		{"Ahmet'ten 5.000 TL tahsilat", []string{"record_collection"}, map[string]string{"customer": "Ahmet", "amount": "5.000", "currency": "TL"}},
		{"Ahmet Yılmaz'dan 500 dolar nakit aldım", []string{"record_collection"}, map[string]string{"customer": "Ahmet Yılmaz", "amount": "500", "currency": "dolar", "method": "nakit"}},
		{"Mehmet'e 2.000 TL ödeme yaptım", []string{"record_payment"}, map[string]string{"customer": "Mehmet", "amount": "2.000"}},
		{"received 300 usd from Ahmet", []string{"record_collection"}, map[string]string{"customer": "Ahmet", "currency": "usd"}},
		{"Ahmet Yılmaz'ın bakiyesi ne?", []string{"query_balance"}, map[string]string{"customer": "Ahmet Yılmaz"}},
		{"stok toner", []string{"query_stock"}, map[string]string{"product": "toner"}},
		{"kritik stok", []string{"query_low_stock"}, nil},
		{"toner stoku ne kadar", []string{"query_stock"}, map[string]string{"product": "toner"}},
		{"vadesi geçen alacaklar", []string{"query_overdue"}, nil},
		{"gün sonu özeti", []string{"daily_summary"}, nil},
		{"Ahmet Yılmaz için 10 paket A4 kağıdı sipariş oluştur", []string{"create_order"}, map[string]string{"customer": "Ahmet Yılmaz", "quantity": "10", "product": "A4 kağıdı"}},
		{"Ahmet'e ekstre at ve vadesi geçenleri hatırlat", []string{"send_statement", "query_overdue"}, map[string]string{"customer": "Ahmet"}},
		{"yardım", []string{"help"}, nil},
	}
	for _, c := range cases {
		got := ParseRules(c.in)
		if len(got) != len(c.caps) {
			t.Errorf("%q: got %d intents %+v, want %v", c.in, len(got), got, c.caps)
			continue
		}
		for i, cp := range c.caps {
			if got[i].Capability != cp {
				t.Errorf("%q: intent %d = %s, want %s", c.in, i, got[i].Capability, cp)
			}
		}
		for k, v := range c.args {
			if got[0].Args[k] != v {
				t.Errorf("%q: arg %s = %q, want %q", c.in, k, got[0].Args[k], v)
			}
		}
	}
	if got := ParseRules("merhaba nasılsın"); got != nil {
		t.Errorf("unexpected intents: %+v", got)
	}
}

func TestParseApproval(t *testing.T) {
	for in, want := range map[string]Approval{"Evet": Yes, "tamam.": Yes, "hayır": No, "iptal": No, "evet ama tutar yanlış, 5.500 olacak": NoApproval} {
		if got := ParseApproval(in); got != want {
			t.Errorf("ParseApproval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDetectLanguage(t *testing.T) {
	for in, want := range map[string]string{"Ahmet'ten 5.000 TL tahsilat": "tr", "received 300 usd from Ahmet": "en", "ما هو رصيد أحمد": "ar"} {
		if got := DetectLanguage(in); got != want {
			t.Errorf("DetectLanguage(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLLMFallback(t *testing.T) {
	p := &Parser{LLM: ai.FakeLLM{}, Capabilities: []CapabilityDoc{{Name: "help"}}}
	res, err := p.Parse(context.Background(), "bu ay ne kadar kâr ettik acaba", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intents[0].Capability != "unknown" || res.Intents[0].Source != "llm" || res.Usage == nil {
		t.Fatalf("unexpected result %+v", res)
	}
}

func BenchmarkParseRules(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ParseRules("Ahmet Yılmaz'dan 5.000 TL nakit tahsilat")
	}
}
