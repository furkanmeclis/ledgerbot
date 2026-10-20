// Package nlu turns user text into intents. The LLM's only job is to
// understand intent and extract arguments; it never computes amounts.
//
// Performance: a deterministic rule parser runs first (microseconds, no cost).
// The LLM is called only when rules do not match, so common phrasings never
// leave the process.
package nlu

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
)

// Intent is one understood request.
type Intent struct {
	Capability string            `json:"capability"`
	Args       map[string]string `json:"args"`
	Source     string            `json:"source"` // "rules" or "llm"
}

// Result is the parse output; multi-step commands yield several intents.
type Result struct {
	Intents  []Intent `json:"intents"`
	Language string   `json:"language"`
	Usage    *ai.Usage
}

// Approval is a yes/no answer recognized without any model.
type Approval int

const (
	NoApproval Approval = iota
	Yes
	No
)

var (
	yesWords = map[string]bool{"evet": true, "onay": true, "onayla": true, "onaylıyorum": true, "tamam": true, "olur": true, "kaydet": true, "yes": true, "ok": true, "okay": true, "نعم": true, "e": true, "✅": true, "👍": true}
	noWords  = map[string]bool{"hayır": true, "hayir": true, "iptal": true, "vazgeç": true, "vazgec": true, "reddet": true, "no": true, "cancel": true, "لا": true, "h": true, "❌": true}
)

// ParseApproval recognizes short approval replies.
func ParseApproval(text string) Approval {
	t := strings.ToLower(strings.TrimFunc(text, func(r rune) bool { return unicode.IsSpace(r) || r == '.' || r == '!' }))
	if len([]rune(t)) > 24 {
		return NoApproval
	}
	switch {
	case yesWords[t]:
		return Yes
	case noWords[t]:
		return No
	}
	return NoApproval
}

// DetectLanguage is a cheap script/keyword heuristic: ar, en or tr.
func DetectLanguage(text string) string {
	var arabic, latin int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Arabic, r):
			arabic++
		case unicode.IsLetter(r):
			latin++
		}
	}
	if arabic > latin {
		return "ar"
	}
	l := " " + strings.ToLower(text) + " "
	if strings.ContainsAny(text, "çğışöüÇĞİŞÖÜ") {
		return "tr"
	}
	en := 0
	for _, w := range []string{" the ", " from ", " received ", " balance ", " stock ", " paid ", " to ", " what ", " how ", " order ", " summary ", " overdue "} {
		if strings.Contains(l, w) {
			en++
		}
	}
	if en > 0 {
		return "en"
	}
	return "tr"
}

// Parser combines rules and an optional LLM fallback.
type Parser struct {
	LLM          ai.LLM // nil disables the fallback
	Capabilities []CapabilityDoc
	// Instruction is the host-provided system prompt (company + role). It is
	// added as context; the JSON output contract always comes from ledgerbot.
	Instruction string
}

// CapabilityDoc describes a capability to the LLM.
type CapabilityDoc struct {
	Name        string
	Description string
	Args        []string
}

// Parse understands text.
func (p *Parser) Parse(ctx context.Context, text string, allowLLM bool) (Result, error) {
	lang := DetectLanguage(text)
	if intents := ParseRules(text); len(intents) > 0 {
		return Result{Intents: intents, Language: lang}, nil
	}
	if !allowLLM || p.LLM == nil {
		return Result{Intents: []Intent{{Capability: "unknown", Source: "rules"}}, Language: lang}, nil
	}
	return p.parseLLM(ctx, text, lang)
}

func (p *Parser) parseLLM(ctx context.Context, text, lang string) (Result, error) {
	var b strings.Builder
	if p.Instruction != "" {
		b.WriteString("Company context (from the host system):\n")
		b.WriteString(p.Instruction)
		b.WriteString("\n\n")
	}
	b.WriteString(systemPrompt)
	for _, c := range p.Capabilities {
		fmt.Fprintf(&b, "- %s: %s (args: %s)\n", c.Name, c.Description, strings.Join(c.Args, ", "))
	}
	comp, err := p.LLM.Complete(ctx, ai.CompletionRequest{
		System: b.String(), JSON: true, MaxTokens: 400,
		Messages: []ai.Message{{Role: "user", Text: text}},
	})
	if err != nil {
		return Result{}, err
	}
	var out struct {
		Intents  []Intent `json:"intents"`
		Language string   `json:"language"`
		// tolerate single-intent answers
		Capability string            `json:"capability"`
		Args       map[string]string `json:"args"`
	}
	if err := json.Unmarshal([]byte(extractJSON(comp.Text)), &out); err != nil {
		return Result{Intents: []Intent{{Capability: "unknown", Source: "llm"}}, Language: lang, Usage: &comp.Usage}, nil
	}
	if len(out.Intents) == 0 && out.Capability != "" {
		out.Intents = []Intent{{Capability: out.Capability, Args: out.Args}}
	}
	known := map[string]bool{"unknown": true}
	for _, c := range p.Capabilities {
		known[c.Name] = true
	}
	res := Result{Language: lang, Usage: &comp.Usage}
	if out.Language == "tr" || out.Language == "en" || out.Language == "ar" {
		res.Language = out.Language
	}
	for _, in := range out.Intents {
		if !known[in.Capability] {
			in.Capability = "unknown"
		}
		in.Source = "llm"
		if in.Args == nil {
			in.Args = map[string]string{}
		}
		res.Intents = append(res.Intents, in)
		if len(res.Intents) == 5 { // bound multi-step chains
			break
		}
	}
	if len(res.Intents) == 0 {
		res.Intents = []Intent{{Capability: "unknown", Source: "llm"}}
	}
	return res, nil
}

const systemPrompt = `You classify messages sent to an accounting assistant used by Turkish SMEs.
Return ONLY JSON: {"intents":[{"capability":"<name>","args":{"<arg>":"<string>"}}],"language":"tr|en|ar"}.
Rules:
- Never compute, convert or invent numbers. Copy amounts exactly as written (e.g. "1.250,50", "5 bin").
- Copy names exactly as written. Use "currency" for currency words (TL, dolar, euro).
- A message may contain several requests joined by "ve"/"and": return one intent per request, in order.
- If nothing fits, use capability "unknown".
Capabilities:
`

func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}

// --- deterministic rules ---------------------------------------------------

const (
	amountRe   = `(\d[\d.,']*(?:\s*(?:bin|milyon))?)`
	currencyRe = `(tl|₺|try|lira|dolar|usd|\$|euro|eur|avro|€|sterlin|gbp|£)?`
	methodRe   = `(nakit|havale|eft|kart|kredi kartı|çek|senet)?`
	suffixRe   = `['’]?\s*(?:d[ae]n|t[ae]n)`
)

type rule struct {
	cap string
	re  *regexp.Regexp
	// names of capture groups mapped to args, in order
	args []string
}

var rules = []rule{
	// memory and knowledge first: a note may itself contain "tahsilat" etc.
	{"repeat_last", regexp.MustCompile(`(?i)^(?:her\s+zamanki\s+gibi|geçen\s+seferki\s+gibi|aynısından|aynısı)(?:[,\s]+` + amountRe + `\s*` + currencyRe + `)?\s*(?:kaydet|gir|yap)?$`), []string{"amount", "currency"}},
	{"add_note", regexp.MustCompile(`(?i)^(?:not\s+al|not\s+et|hatırla|unutma|note)\s*[:,-]?\s+(.+)$`), []string{"text"}},
	{"search_knowledge", regexp.MustCompile(`(?i)^(?:notlarda|notlarımda|hafızada|kayıtlarda)\s+(.+?)(?:\s+(?:ara|bul|ne\s+yazıyor|var\s+m[ıi]))?\??$`), []string{"query"}},
	{"search_knowledge", regexp.MustCompile(`(?i)^(.+?)\s+hakkında\s+(?:ne\s+biliyorsun|not\s+var\s+m[ıi]|bilgi)\??$`), []string{"query"}},
	// "Ahmet'ten 5.000 TL tahsilat", "Ahmet Yılmaz'dan 500 dolar nakit aldım"
	{"record_collection", regexp.MustCompile(`(?i)^(.+?)` + suffixRe + `\s+` + amountRe + `\s*` + currencyRe + `\s*` + methodRe + `\s*(?:tahsilat(?:\s+(?:yaptım|aldım|yap|al|gir|kaydet))?|tahsil\s+ettim|aldım|alındı)\b`), []string{"customer", "amount", "currency", "method"}},
	// "5.000 TL tahsilat Ahmet'ten"
	{"record_collection", regexp.MustCompile(`(?i)^` + amountRe + `\s*` + currencyRe + `\s*` + methodRe + `\s*tahsilat\s+(.+?)` + suffixRe + `$`), []string{"amount", "currency", "method", "customer"}},
	// "received 500 usd from Ahmet"
	{"record_collection", regexp.MustCompile(`(?i)^(?:received|got|collected)\s+` + amountRe + `\s*` + currencyRe + `\s+from\s+(.+)$`), []string{"amount", "currency", "customer"}},
	// "Mehmet'e 2.000 TL ödeme yaptım", "Mehmet'e 2.000 TL ödedim"
	{"record_payment", regexp.MustCompile(`(?i)^(.+?)['’]?\s*(?:y?[ae])\s+` + amountRe + `\s*` + currencyRe + `\s*` + methodRe + `\s*(?:ödeme(?:\s+(?:yaptım|yap|gir|kaydet))?|ödedim|gönderdim)\b`), []string{"customer", "amount", "currency", "method"}},
	// "paid 300 eur to Mehmet"
	{"record_payment", regexp.MustCompile(`(?i)^(?:paid|sent)\s+` + amountRe + `\s*` + currencyRe + `\s+to\s+(.+)$`), []string{"amount", "currency", "customer"}},
	// "Ahmet Yılmaz için 10 paket A4 kağıdı sipariş oluştur"
	{"create_order", regexp.MustCompile(`(?i)^(.+?)\s+için\s+(\d+(?:[.,]\d+)?)\s*(?:adet|paket|kutu|kg|litre|metre|torba|çuval|takım)?\s+(.+?)\s+sipariş`), []string{"customer", "quantity", "product"}},
	// "Ahmet'in bakiyesi", "Ahmet Yılmaz'ın bakiyesi ne", "bakiye Ahmet"
	{"query_balance", regexp.MustCompile(`(?i)^(.+?)['’]?\s*(?:n?[ıiuü]n)\s+(?:bakiye(?:si)?|borcu|hesab[ıi]|ekstresi)`), []string{"customer"}},
	{"query_balance", regexp.MustCompile(`(?i)^(?:bakiye|cari|borç)\s+(.+?)\??$`), []string{"customer"}},
	{"query_balance", regexp.MustCompile(`(?i)^(?:balance|statement)\s+(?:of|for)\s+(.+?)\??$`), []string{"customer"}},
	// "Ahmet'e ekstre at/gönder"
	{"send_statement", regexp.MustCompile(`(?i)^(.+?)['’]?\s*(?:y?[ae])\s+ekstre\s*(?:at|gönder|yolla)`), []string{"customer"}},
	// stock (low-stock first: "kritik stok" must not read as product "kritik")
	{"query_low_stock", regexp.MustCompile(`(?i)(?:kritik stok|azalan stok|stoğu azalan|low stock|biten ürün)`), nil},
	{"query_stock", regexp.MustCompile(`(?i)^(?:stok(?:ta)?|stock)\s+(.+?)(?:\s+var\s+m[ıi])?\??$`), []string{"product"}},
	{"query_stock", regexp.MustCompile(`(?i)^(.+?)\s+(?:stok(?:u|ta)?|stock)(?:\s+(?:ne kadar|kaç|var m[ıi]|durumu))?\??$`), []string{"product"}},
	// overdue
	{"query_overdue", regexp.MustCompile(`(?i)(?:vadesi\s+geçen|vadesi\s+gecen|geciken\s+(?:alacak|ödeme|fatura)|overdue)`), nil},
	// reports
	{"query_report", regexp.MustCompile(`(?i)(?:haftalık\s+(?:rapor|kâr|kar)|kâr\s*(?:-|ve)?\s*zarar|kar\s*(?:-|ve)?\s*zarar|weekly\s+(?:report|p&l|pnl))`), nil},
	{"query_report", regexp.MustCompile(`(?i)(?:kdv\s+(?:tahmini|durumu|raporu)|aylık\s+kdv|ne\s+kadar\s+kdv|vat\s+estimate)`), nil},
	{"query_report", regexp.MustCompile(`(?i)(?:sabah\s+özeti|günlük\s+rapor|dünün\s+özeti|daily\s+digest)`), nil},
	// summary
	{"daily_summary", regexp.MustCompile(`(?i)(?:gün\s*sonu|günün\s+özeti|bugünün\s+özeti|günlük\s+özet|bugün\s+ne\s+sattık|kasa\s+durumu|daily\s+summary|today'?s\s+summary)`), nil},
	// help
	{"help", regexp.MustCompile(`(?i)^(?:yardım|help|ne\s+yapabilirsin|neler\s+yapabilirsin|menü|komutlar)\??$`), nil},
}

var chainSplit = regexp.MustCompile(`(?i)\s+(?:ve(?:\s+sonra)?|sonra|ardından|and(?:\s+then)?)\s+`)

// ParseRules returns intents for known phrasings, or nil.
// Chained commands ("X ve Y") are split and each part must match a rule.
func ParseRules(text string) []Intent {
	text = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(text), ".!"))
	if text == "" {
		return nil
	}
	// Chains first: a chain is accepted only if every part matches a rule, so
	// names containing "ve" ("Ali ve Oğulları") still fall through to one intent.
	if parts := chainSplit.Split(text, 5); len(parts) >= 2 {
		out := make([]Intent, 0, len(parts))
		for _, p := range parts {
			in, ok := matchOne(strings.TrimSpace(p))
			if !ok {
				out = nil
				break
			}
			out = append(out, in)
		}
		if out != nil {
			return out
		}
	}
	if in, ok := matchOne(text); ok {
		return []Intent{in}
	}
	return nil
}

func matchOne(text string) (Intent, bool) {
	for _, r := range rules {
		m := r.re.FindStringSubmatch(text)
		if m == nil {
			continue
		}
		in := Intent{Capability: r.cap, Args: make(map[string]string, len(r.args)), Source: "rules"}
		for i, name := range r.args {
			if v := strings.TrimSpace(m[i+1]); v != "" {
				in.Args[name] = v
			}
		}
		if r.cap == "query_report" {
			in.Args["kind"] = reportKind(text)
		}
		return in, true
	}
	return Intent{}, false
}

func reportKind(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "kdv") || strings.Contains(t, "vat"):
		return "monthly_vat"
	case strings.Contains(t, "sabah") || strings.Contains(t, "günlük") || strings.Contains(t, "dünün") || strings.Contains(t, "daily"):
		return "daily_digest"
	}
	return "weekly_pnl"
}
