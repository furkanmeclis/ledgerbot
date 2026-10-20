package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/einvoice"
)

// receipt is the JSON shape the extraction model must return. Amounts are
// strings copied from the document; code parses and re-computes them.
type receipt struct {
	Vendor     string        `json:"vendor"`
	VendorTax  string        `json:"vendor_tax_id"`
	DocumentNo string        `json:"document_no"`
	Date       string        `json:"date"`
	Currency   string        `json:"currency"`
	Lines      []receiptLine `json:"lines"`
	Total      string        `json:"total"`
	Confidence float64       `json:"confidence"`
}

type receiptLine struct {
	Name      string  `json:"name"`
	Quantity  float64 `json:"quantity"`
	Unit      string  `json:"unit"`
	UnitPrice string  `json:"unit_price"`
	VATRate   int     `json:"vat_rate"`
}

const extractionPrompt = `Extract the purchase document (receipt, invoice, fiş, fatura) in the image.
Return ONLY JSON: {"vendor":"","vendor_tax_id":"","document_no":"","date":"YYYY-MM-DD","currency":"TRY",
"lines":[{"name":"","quantity":1,"unit":"","unit_price":"<as printed, VAT excluded>","vat_rate":20}],
"total":"<grand total as printed>","confidence":0.0}
Copy numbers exactly as printed. Do not compute anything. One entry per item line. confidence is 0..1.`

// runExpense handles photos, PDFs and e-invoice XML. Structured XML is parsed
// without AI; images and PDFs go to the extraction model.
func runExpense(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	m := c.Msg.Media
	if m == nil || len(m.Data) == 0 {
		return Outcome{Text: T(c.Lang, "unsupported_media")}, nil
	}
	if strings.Contains(m.MIME, "xml") || strings.HasSuffix(strings.ToLower(m.Filename), ".xml") || einvoice.Looks(m.Data) {
		return expenseFromUBL(e, c, m.Data)
	}
	if !strings.HasPrefix(m.MIME, "image/") && m.MIME != "application/pdf" {
		return Outcome{Text: T(c.Lang, "unsupported_media")}, nil
	}
	if !e.quotaOK(ctx, c.Snap) {
		return Outcome{Text: T(c.Lang, "quota")}, nil
	}
	// Exact re-sends are caught before spending tokens.
	sum := sha256.Sum256(m.Data)
	rawFP := "raw:" + hex.EncodeToString(sum[:])

	override := c.Snap.Settings.Providers.Extraction
	llm, err := e.AI.LLM(ai.PurposeExtraction, override)
	if err != nil {
		return Outcome{}, err
	}
	comp, err := llm.Complete(ctx, ai.CompletionRequest{
		System: extractionPrompt, JSON: true, MaxTokens: 1500,
		Messages: []ai.Message{{Role: "user", Text: "Belgeyi çıkar."}},
		Images:   []domain.Media{*m},
	})
	if err != nil {
		return Outcome{}, err
	}
	e.recordUsage(ctx, c.Snap, ai.PurposeExtraction, override, comp.Usage)
	var r receipt
	if err := json.Unmarshal([]byte(extractJSONObject(comp.Text)), &r); err != nil || (r.Total == "" && len(r.Lines) == 0) {
		return Outcome{Text: T(c.Lang, "unreadable")}, nil
	}

	rec := &dataprovider.Record{
		Kind: dataprovider.RecordExpense, Vendor: r.Vendor, VendorTax: r.VendorTax, DocumentNo: r.DocumentNo,
		Date: dateOr(r.Date, e.now()), Note: "receipt",
	}
	var computed, vat domain.Money
	for _, l := range r.Lines {
		up, _ := domain.ParseAmount(l.UnitPrice)
		qty := int64(l.Quantity * 1000)
		if qty <= 0 {
			qty = 1000
		}
		net := domain.Money(int64(up) * qty / 1000)
		lvat := domain.Money(int64(net) * int64(l.VATRate) / 100)
		rec.Lines = append(rec.Lines, dataprovider.Line{Name: l.Name, Quantity: qty, Unit: l.Unit, UnitPrice: up, VATRate: l.VATRate, Total: net + lvat})
		computed += net + lvat
		vat += lvat
	}
	stated, _ := domain.ParseAmount(r.Total)
	rec.Amount, rec.VAT = stated, vat
	if stated == 0 {
		rec.Amount = computed
	}
	var warnings []string
	if stated != 0 && computed != 0 && absMoney(stated-computed) > 100 { // > 1 TL difference
		warnings = append(warnings, fmt.Sprintf("• kalem toplamı %s, belgedeki toplam %s", computed, stated))
	}
	if r.Confidence > 0 && r.Confidence < 0.6 {
		warnings = append(warnings, "• okuma güveni düşük ("+strconv.Itoa(int(r.Confidence*100))+"%)")
	}
	vendor := r.Vendor
	if vendor == "" {
		vendor = "?"
	}
	out := Outcome{
		Record:  rec,
		Summary: T(c.Lang, "expense", vendor, len(rec.Lines), vat.String(), rec.Amount.String()),
	}
	if r.DocumentNo != "" || r.VendorTax != "" {
		out.Fingerprint = fingerprint("doc", r.VendorTax, r.DocumentNo, strconv.FormatInt(int64(rec.Amount), 10))
	} else {
		out.Fingerprint = rawFP
	}
	if len(warnings) > 0 {
		out.Text = T(c.Lang, "low_confidence", strings.Join(warnings, "\n"))
	}
	return out, nil
}

func expenseFromUBL(e *Engine, c *Call, data []byte) (Outcome, error) {
	doc, err := einvoice.Parse(data)
	if err != nil {
		return Outcome{Text: T(c.Lang, "unreadable")}, nil
	}
	rec := &dataprovider.Record{
		Kind: dataprovider.RecordExpense, Vendor: doc.Supplier.Name, VendorTax: doc.Supplier.TaxID, DocumentNo: doc.ID,
		Date: doc.IssueDate, Amount: doc.Payable, VAT: doc.VAT, Lines: doc.Lines, Note: "e-fatura " + doc.UUID,
	}
	if rec.Date.IsZero() {
		rec.Date = e.now()
	}
	return Outcome{
		Record:      rec,
		Summary:     T(c.Lang, "einvoice", doc.ID, doc.Supplier.Name, len(doc.Lines), doc.VAT.String(), doc.Payable.String()),
		Fingerprint: fingerprint("ubl", doc.UUID),
	}, nil
}

func absMoney(m domain.Money) domain.Money {
	if m < 0 {
		return -m
	}
	return m
}

func extractJSONObject(s string) string {
	if i, j := strings.IndexByte(s, '{'), strings.LastIndexByte(s, '}'); i >= 0 && j > i {
		return s[i : j+1]
	}
	return s
}
