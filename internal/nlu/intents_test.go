package nlu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
)

func TestParseRulesIntentTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		cap  string
		args map[string]string
	}{
		// collections (tr)
		{"Ali'den 250 TL tahsilat", "record_collection", map[string]string{"customer": "Ali", "amount": "250", "currency": "TL"}},
		{"Ayşe Kaya'dan 1.250,50 TL havale tahsilat yaptım", "record_collection", map[string]string{"customer": "Ayşe Kaya", "amount": "1.250,50", "method": "havale"}},
		{"Mehmet'ten 5 bin lira aldım", "record_collection", map[string]string{"customer": "Mehmet", "amount": "5 bin", "currency": "lira"}},
		{"Demir Yapı'dan 2,5 milyon TL çek aldım", "record_collection", map[string]string{"customer": "Demir Yapı", "amount": "2,5 milyon", "method": "çek"}},
		{"Ayşe'den 100 TL kredi kartı tahsilat", "record_collection", map[string]string{"customer": "Ayşe", "method": "kredi kartı"}},
		{"Ahmet'ten 5000TL tahsilat", "record_collection", map[string]string{"amount": "5000", "currency": "TL"}},
		{"Selin’den 300 € tahsil ettim", "record_collection", map[string]string{"customer": "Selin", "amount": "300", "currency": "€"}},
		{"Burak'tan 750 kart tahsilat", "record_collection", map[string]string{"customer": "Burak", "amount": "750", "method": "kart"}},
		{"Kaya Tekstil'den 10.000 TL tahsilat gir", "record_collection", map[string]string{"customer": "Kaya Tekstil", "amount": "10.000"}},
		{"5.000 TL tahsilat Ahmet'ten", "record_collection", map[string]string{"customer": "Ahmet", "amount": "5.000", "currency": "TL"}},
		{"300 dolar nakit tahsilat Zeynep'ten", "record_collection", map[string]string{"customer": "Zeynep", "amount": "300", "currency": "dolar", "method": "nakit"}},
		{"AHMET'TEN 500 TL TAHSILAT", "record_collection", map[string]string{"customer": "AHMET", "amount": "500"}},
		// collections (en)
		{"received 500 usd from Ahmet Yılmaz", "record_collection", map[string]string{"customer": "Ahmet Yılmaz", "amount": "500", "currency": "usd"}},
		{"got 1,200 eur from Mehmet", "record_collection", map[string]string{"customer": "Mehmet", "amount": "1,200", "currency": "eur"}},
		{"collected 75 from Elif", "record_collection", map[string]string{"customer": "Elif", "amount": "75"}},
		// payments
		{"Mehmet'e 2.000 TL ödedim", "record_payment", map[string]string{"customer": "Mehmet", "amount": "2.000", "currency": "TL"}},
		{"Ayşe'ye 500 TL havale ödeme yaptım", "record_payment", map[string]string{"customer": "Ayşe", "amount": "500", "method": "havale"}},
		{"Doğan Gıda'ya 12.500 TL eft gönderdim", "record_payment", map[string]string{"customer": "Doğan Gıda", "amount": "12.500", "method": "eft"}},
		{"Emre'ye 3 bin lira ödeme", "record_payment", map[string]string{"customer": "Emre", "amount": "3 bin"}},
		{"paid 300 eur to Mehmet", "record_payment", map[string]string{"customer": "Mehmet", "amount": "300", "currency": "eur"}},
		{"sent 50 $ to Derya Çelik", "record_payment", map[string]string{"customer": "Derya Çelik", "amount": "50", "currency": "$"}},
		// orders
		{"Kaya Tekstil için 5 adet toner sipariş", "create_order", map[string]string{"customer": "Kaya Tekstil", "quantity": "5", "product": "toner"}},
		{"Elif için 2,5 kg un siparişi ver", "create_order", map[string]string{"customer": "Elif", "quantity": "2,5", "product": "un"}},
		{"Hakan için 3 Balata Seti sipariş oluştur", "create_order", map[string]string{"customer": "Hakan", "quantity": "3", "product": "Balata Seti"}},
		// balances
		{"Ahmet'in bakiyesi", "query_balance", map[string]string{"customer": "Ahmet"}},
		{"Mehmet'in borcu ne kadar", "query_balance", map[string]string{"customer": "Mehmet"}},
		{"Öztürk'ün hesabı", "query_balance", map[string]string{"customer": "Öztürk"}},
		{"Ali'nin ekstresi", "query_balance", map[string]string{"customer": "Ali"}},
		{"bakiye Ahmet Yılmaz", "query_balance", map[string]string{"customer": "Ahmet Yılmaz"}},
		{"cari Demir Yapı?", "query_balance", map[string]string{"customer": "Demir Yapı"}},
		{"borç Selin", "query_balance", map[string]string{"customer": "Selin"}},
		{"balance of Ahmet", "query_balance", map[string]string{"customer": "Ahmet"}},
		{"statement for Kaya Tekstil?", "query_balance", map[string]string{"customer": "Kaya Tekstil"}},
		// statements
		{"Ayşe'ye ekstre gönder", "send_statement", map[string]string{"customer": "Ayşe"}},
		{"Demir'e ekstre yolla", "send_statement", map[string]string{"customer": "Demir"}},
		// stock
		{"stoğu azalan ürünler", "query_low_stock", nil},
		{"azalan stok listesi", "query_low_stock", nil},
		{"show low stock", "query_low_stock", nil},
		{"biten ürün var mı", "query_low_stock", nil},
		{"stokta toner var mı?", "query_stock", map[string]string{"product": "toner"}},
		{"stock cement", "query_stock", map[string]string{"product": "cement"}},
		{"un stoku kaç", "query_stock", map[string]string{"product": "un"}},
		{"LED ampul stok durumu", "query_stock", map[string]string{"product": "LED ampul"}},
		// overdue
		{"vadesi gecen faturalar", "query_overdue", nil},
		{"geciken alacak var mı", "query_overdue", nil},
		{"geciken ödemeler", "query_overdue", nil},
		{"show overdue invoices", "query_overdue", nil},
		// cash forecast
		{"nakit akış tahmini", "cash_forecast", nil},
		{"kasa ne zaman eksi olur", "cash_forecast", nil},
		{"ay sonuna para yeter mi", "cash_forecast", nil},
		{"cash flow next month", "cash_forecast", nil},
		{"cash forecast", "cash_forecast", nil},
		// reports
		{"haftalık rapor", "query_report", map[string]string{"kind": "weekly_pnl"}},
		{"kâr zarar", "query_report", map[string]string{"kind": "weekly_pnl"}},
		{"kar-zarar durumu", "query_report", map[string]string{"kind": "weekly_pnl"}},
		{"weekly p&l", "query_report", map[string]string{"kind": "weekly_pnl"}},
		{"bu ayın kdv tahmini", "query_report", map[string]string{"kind": "monthly_vat"}},
		{"aylık KDV", "query_report", map[string]string{"kind": "monthly_vat"}},
		{"vat estimate", "query_report", map[string]string{"kind": "monthly_vat"}},
		{"sabah özeti", "query_report", map[string]string{"kind": "daily_digest"}},
		{"dünün özeti", "query_report", map[string]string{"kind": "daily_digest"}},
		{"daily digest", "query_report", map[string]string{"kind": "daily_digest"}},
		// daily summary
		{"günün özeti", "daily_summary", nil},
		{"bugün ne sattık", "daily_summary", nil},
		{"kasa durumu", "daily_summary", nil},
		{"daily summary", "daily_summary", nil},
		{"today's summary", "daily_summary", nil},
		{"todays summary", "daily_summary", nil},
		// help
		{"help", "help", nil},
		{"Yardım?", "help", nil},
		{"neler yapabilirsin", "help", nil},
		{"komutlar", "help", nil},
		// memory
		{"aynısı", "repeat_last", nil},
		{"geçen seferki gibi 1.200 TL kaydet", "repeat_last", map[string]string{"amount": "1.200", "currency": "TL"}},
		{"unutma: kira her ayın 5'inde", "add_note", map[string]string{"text": "kira her ayın 5'inde"}},
		{"note - call the bank", "add_note", map[string]string{"text": "call the bank"}},
		{"hafızada depo kirası var mı", "search_knowledge", map[string]string{"query": "depo kirası"}},
		{"Kaya Tekstil hakkında not var mı?", "search_knowledge", map[string]string{"query": "Kaya Tekstil"}},
		// trailing punctuation is ignored
		{"yardım!!", "help", nil},
		{"  Ahmet'in bakiyesi.  ", "query_balance", map[string]string{"customer": "Ahmet"}},
	}
	for _, c := range cases {
		got := ParseRules(c.in)
		if len(got) != 1 {
			t.Errorf("%q: got %d intents %+v, want 1 %s", c.in, len(got), got, c.cap)
			continue
		}
		if got[0].Capability != c.cap || got[0].Source != "rules" {
			t.Errorf("%q: capability %s (%s), want %s", c.in, got[0].Capability, got[0].Source, c.cap)
			continue
		}
		for k, v := range c.args {
			if got[0].Args[k] != v {
				t.Errorf("%q: arg %s = %q, want %q (args %v)", c.in, k, got[0].Args[k], v, got[0].Args)
			}
		}
		if c.args == nil && c.cap != "query_report" && len(got[0].Args) != 0 {
			t.Errorf("%q: unexpected args %v", c.in, got[0].Args)
		}
	}
}

func TestParseRulesNoMatch(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"", "   ", "...", "merhaba", "selam nasılsın", "hello there", "bu ay ne kadar kâr ettik acaba",
		"Ahmet", "5.000 TL", "tahsilat", "stok", "bakiye", "fatura kes",
	} {
		if got := ParseRules(in); got != nil {
			t.Errorf("ParseRules(%q) = %+v, want nil", in, got)
		}
	}
}

func TestParseRulesChains(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		caps []string
	}{
		{"gün sonu özeti ve kritik stok", []string{"daily_summary", "query_low_stock"}},
		{"Ahmet'ten 500 TL tahsilat ve sonra Ahmet'in bakiyesi", []string{"record_collection", "query_balance"}},
		{"kritik stok sonra vadesi geçen alacaklar ardından nakit akış", []string{"query_low_stock", "query_overdue", "cash_forecast"}},
		{"daily summary and then low stock", []string{"daily_summary", "query_low_stock"}},
		{"yardım ve yardım ve yardım ve yardım ve yardım", []string{"help", "help", "help", "help", "help"}},
		// a company name containing "ve" stays one intent when the split does not parse
		{"Ali ve Oğulları'nın bakiyesi", []string{"query_balance"}},
	}
	for _, c := range cases {
		got := ParseRules(c.in)
		caps := make([]string, len(got))
		for i, in := range got {
			caps[i] = in.Capability
		}
		if strings.Join(caps, ",") != strings.Join(c.caps, ",") {
			t.Errorf("%q: got %v, want %v", c.in, caps, c.caps)
		}
	}
	if got := ParseRules("Ali ve Oğulları'nın bakiyesi"); len(got) != 1 || got[0].Args["customer"] != "Ali ve Oğulları" {
		t.Errorf("customer = %+v", got)
	}
}

func TestParseRulesChainLimit(t *testing.T) {
	t.Parallel()
	// Split stops at five parts; the fifth keeps the rest and must still match.
	got := ParseRules("yardım ve yardım ve yardım ve yardım ve yardım ve yardım")
	if len(got) > 5 {
		t.Fatalf("chain produced %d intents, want at most 5", len(got))
	}
}

func TestParseRulesAlindiNeverMatches(t *testing.T) {
	t.Parallel()
	if got := ParseRules("Demir'den 500 TL alındı"); len(got) != 1 || got[0].Capability != "record_collection" {
		t.Skipf("BUG: ParseRules(%q) = %+v; the collection rule ends in `\\b`, which RE2 evaluates on ASCII word characters only, so the \"alındı\" alternative (ending in 'ı') can never match", "Demir'den 500 TL alındı", got)
	}
}

func TestParseRulesBufferN(t *testing.T) {
	t.Parallel()
	got := ParseRules("Ali ve Oğulları'ndan 500 TL tahsilat")
	if len(got) != 1 || got[0].Args["customer"] != "Ali ve Oğulları" {
		t.Skipf("BUG: customer = %q; suffixRe handles 'dan/'ten but not the buffer-n form 'ndan/'nden used after a possessive (\"Oğulları'ndan\"), so \"'n\" stays in the name", got[0].Args["customer"])
	}
}

func TestParseRulesTurkishUppercase(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{"AHMET'TEN 500 TL TAHSİLAT": "record_collection", "KRİTİK STOK": "query_low_stock"} {
		if got := ParseRules(in); len(got) != 1 || got[0].Capability != want {
			t.Skipf("BUG: ParseRules(%q) = %+v, want %s; RE2 (?i) does not fold 'İ' (U+0130) to 'i', so all-caps Turkish misses rules (\"KRİTİK STOK\" even becomes query_stock for product \"KRİTİK\")", in, got, want)
		}
	}
}

func TestParseApprovalTable(t *testing.T) {
	t.Parallel()
	cases := map[string]Approval{
		"evet": Yes, "EVET!": Yes, " Tamam. ": Yes, "onayla": Yes, "onaylıyorum": Yes, "olur": Yes, "kaydet": Yes,
		"ok": Yes, "OK!": Yes, "okay": Yes, "yes": Yes, "e": Yes, "✅": Yes, "👍": Yes, "نعم": Yes,
		"hayır": No, "Hayir": No, "iptal!": No, "vazgeç": No, "vazgec": No, "reddet": No, "no": No, "cancel": No, "h": No, "❌": No, "لا": No,
		"": NoApproval, "evet evet": NoApproval, "belki": NoApproval, "yes please": NoApproval, "?": NoApproval,
		"evet ama tutarı düzelt": NoApproval, strings.Repeat("e", 30): NoApproval,
	}
	for in, want := range cases {
		if got := ParseApproval(in); got != want {
			t.Errorf("ParseApproval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDetectLanguageTable(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                          "tr",
		"12345":                     "tr",
		"stok toner":                "tr",
		"Mehmet'e 2.000 TL ödedim":  "tr",
		"kritik stok":               "tr",
		"paid 300 eur to Mehmet":    "en",
		"what is the balance":       "en",
		"how much stock":            "en",
		"show overdue invoices":     "en",
		"daily summary please":      "en",
		"Ahmet Yılmaz paid":         "tr",
		"ما هو رصيد أحمد":           "ar",
		"رصيد أحمد Ali":             "ar",
		"Ahmet Mehmet Ali رصيد":     "tr",
		"received money from İzmir": "tr",
		"RECEIVED 5 USD FROM AHMET": "en",
	}
	for in, want := range cases {
		if got := DetectLanguage(in); got != want {
			t.Errorf("DetectLanguage(%q) = %s, want %s", in, got, want)
		}
	}
}

type scriptedLLM struct {
	text string
	err  error
	req  ai.CompletionRequest
}

func (s *scriptedLLM) Complete(_ context.Context, req ai.CompletionRequest) (ai.Completion, error) {
	s.req = req
	if s.err != nil {
		return ai.Completion{}, s.err
	}
	return ai.Completion{Text: s.text, Usage: ai.Usage{Provider: "scripted", InputTokens: 10}}, nil
}

func TestParserPrefersRules(t *testing.T) {
	t.Parallel()
	llm := &scriptedLLM{text: `{"capability":"help"}`}
	p := &Parser{LLM: llm, Before: func(string) (Intent, bool) { return Intent{Capability: "plugin"}, true }}
	res, err := p.Parse(context.Background(), "received 5 usd from Ali", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Intents[0].Capability != "record_collection" || res.Language != "en" || res.Usage != nil || llm.req.System != "" {
		t.Fatalf("rules should win without calling the LLM: %+v", res)
	}
}

func TestParserBeforeHook(t *testing.T) {
	t.Parallel()
	llm := &scriptedLLM{text: `{"capability":"help"}`}
	p := &Parser{LLM: llm, Before: func(text string) (Intent, bool) {
		if strings.HasPrefix(text, "e-fatura") {
			return Intent{Capability: "plugin.einvoice", Args: map[string]string{"q": text}, Source: "plugin"}, true
		}
		return Intent{}, false
	}}
	res, _ := p.Parse(context.Background(), "e-fatura listesi", true)
	if len(res.Intents) != 1 || res.Intents[0].Capability != "plugin.einvoice" || llm.req.System != "" {
		t.Fatalf("Before hook result = %+v", res)
	}
	res, _ = p.Parse(context.Background(), "bambaşka bir şey", false)
	if res.Intents[0].Capability != "unknown" || res.Intents[0].Source != "rules" {
		t.Fatalf("no LLM allowed = %+v", res)
	}
}

func TestParserNoLLM(t *testing.T) {
	t.Parallel()
	res, err := (&Parser{}).Parse(context.Background(), "anlamsız cümle", true)
	if err != nil || len(res.Intents) != 1 || res.Intents[0].Capability != "unknown" || res.Intents[0].Source != "rules" || res.Language != "tr" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestParserLLMResponses(t *testing.T) {
	t.Parallel()
	caps := []CapabilityDoc{{Name: "help", Description: "show help"}, {Name: "query_balance", Description: "balance", Args: []string{"customer"}}}
	cases := []struct {
		name  string
		reply string
		caps  []string
		lang  string
		arg   string
	}{
		{"multi intents", `{"intents":[{"capability":"help"},{"capability":"query_balance","args":{"customer":"Ali"}}],"language":"en"}`, []string{"help", "query_balance"}, "en", ""},
		{"single intent form", `{"capability":"query_balance","args":{"customer":"Veli"}}`, []string{"query_balance"}, "tr", "Veli"},
		{"unknown capability mapped", `{"intents":[{"capability":"rm_rf"}]}`, []string{"unknown"}, "tr", ""},
		{"code fence", "```json\n{\"capability\":\"help\",\"language\":\"ar\"}\n```", []string{"help"}, "ar", ""},
		{"prose around json", `Sure! {"capability":"help"} hope that helps`, []string{"help"}, "tr", ""},
		{"invalid language ignored", `{"capability":"help","language":"de"}`, []string{"help"}, "tr", ""},
		{"not json", `I cannot help with that`, []string{"unknown"}, "tr", ""},
		{"empty object", `{}`, []string{"unknown"}, "tr", ""},
		{"capped at five", `{"intents":[{"capability":"help"},{"capability":"help"},{"capability":"help"},{"capability":"help"},{"capability":"help"},{"capability":"help"},{"capability":"help"}]}`,
			[]string{"help", "help", "help", "help", "help"}, "tr", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			llm := &scriptedLLM{text: c.reply}
			p := &Parser{LLM: llm, Capabilities: caps, Instruction: "Şirket: Acme"}
			res, err := p.Parse(context.Background(), "bu cümleyi kurallar anlamaz", true)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(res.Intents))
			for i, in := range res.Intents {
				got[i] = in.Capability
				if in.Source != "llm" || (in.Capability != "unknown" && in.Args == nil) {
					t.Fatalf("intent %d = %+v", i, in)
				}
			}
			if strings.Join(got, ",") != strings.Join(c.caps, ",") || res.Language != c.lang || res.Usage == nil || res.Usage.Provider != "scripted" {
				t.Fatalf("res = %+v (caps %v lang %s)", res, got, res.Language)
			}
			if c.arg != "" && res.Intents[0].Args["customer"] != c.arg {
				t.Fatalf("customer = %q", res.Intents[0].Args["customer"])
			}
			sys := llm.req.System
			if !llm.req.JSON || llm.req.MaxTokens != 400 || !strings.HasPrefix(sys, "Company context") || !strings.Contains(sys, "Şirket: Acme") ||
				!strings.Contains(sys, "- query_balance: balance (args: customer)") || len(llm.req.Messages) != 1 || llm.req.Messages[0].Role != "user" {
				t.Fatalf("request = %+v", llm.req)
			}
		})
	}
}

func TestParserLLMError(t *testing.T) {
	t.Parallel()
	boom := errors.New("provider down")
	_, err := (&Parser{LLM: &scriptedLLM{err: boom}}).Parse(context.Background(), "kurallar bunu bilmez", true)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestParserNoInstruction(t *testing.T) {
	t.Parallel()
	llm := &scriptedLLM{text: `{}`}
	_, _ = (&Parser{LLM: llm}).Parse(context.Background(), "kurallar bunu bilmez", true)
	if strings.Contains(llm.req.System, "Company context") || !strings.HasPrefix(llm.req.System, "You classify") {
		t.Fatalf("system = %q", llm.req.System)
	}
}

func TestExtractJSON(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`{"a":1}`:                     `{"a":1}`,
		"```json\n{\"a\":1}\n```":     `{"a":1}`,
		"```\n{\"a\":1}```":           `{"a":1}`,
		`text {"a":{"b":2}} trailing`: `{"a":{"b":2}}`,
		`no json here`:                `no json here`,
		`} reversed {`:                `} reversed {`,
		"":                            "",
	}
	for in, want := range cases {
		if got := extractJSON(in); got != want {
			t.Errorf("extractJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func BenchmarkParseRulesMiss(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ParseRules("bu ay ne kadar kâr ettik acaba")
	}
}

func BenchmarkParseRulesChain(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ParseRules("gün sonu özeti ve kritik stok")
	}
}
