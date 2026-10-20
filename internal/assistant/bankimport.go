package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/bank"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

const statementPrompt = `The document is a bank account statement. Return ONLY JSON:
{"transactions":[{"date":"DD.MM.YYYY","description":"","amount":"<as printed, negative for money out>","reference":""}]}
Copy every transaction row exactly; do not compute or skip anything.`

var invoiceNoRe = regexp.MustCompile(`(?i)\b([A-Z]{2,4}\d{6,})\b`)

// runBankImport turns a statement (CSV, XLSX or PDF) into one batch draft of
// collections and payments matched to customers and open invoices.
func runBankImport(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	m := c.Msg.Media
	if m == nil || len(m.Data) == 0 {
		return Outcome{Text: T(c.Lang, "unsupported_media")}, nil
	}
	txs, err := parseStatement(ctx, e, c, m)
	if err != nil || len(txs) == 0 {
		return Outcome{Text: T(c.Lang, "bank_unreadable")}, nil //nolint:nilerr // unreadable input is a user-facing answer
	}
	company := c.Actor.CompanyID
	customers, err := e.Data.FindCustomers(ctx, company, "", 0)
	if err != nil {
		return Outcome{}, err
	}
	open, err := e.Data.Overdue(ctx, company, 0)
	if err != nil {
		return Outcome{}, err
	}

	allOpen := open // invoice numbers stay recognizable after earlier lines paid them
	batch := &dataprovider.Record{Kind: dataprovider.RecordBatch, Date: e.now(), Note: "bank statement " + m.Filename}
	var lines, unmatched []string
	var in, out domain.Money
	for _, tx := range txs {
		cust, ok := matchCustomer(customers, allOpen, open, tx)
		if !ok {
			unmatched = append(unmatched, fmt.Sprintf("• %s %s %s", tx.Date.Format("02.01"), tx.Amount, trim(tx.Description, 40)))
			continue
		}
		rec := dataprovider.Record{CustomerID: cust.ID, Date: tx.Date, Method: "havale", Note: tx.Description, Reference: tx.Reference}
		if tx.Amount > 0 {
			rec.Kind, rec.Amount = dataprovider.RecordCollection, tx.Amount
			var custOpen []dataprovider.OverdueItem
			for _, inv := range open {
				if inv.CustomerID == cust.ID {
					custOpen = append(custOpen, inv)
				}
			}
			rec.Allocations, _ = allocate(custOpen, rec.Amount)
			open = consume(open, rec.Allocations)
			in += rec.Amount
		} else {
			rec.Kind, rec.Amount = dataprovider.RecordPayment, -tx.Amount
			out += rec.Amount
		}
		batch.Items = append(batch.Items, rec)
		line := fmt.Sprintf("• %s %s %s", tx.Date.Format("02.01"), cust.Name, tx.Amount)
		for _, a := range rec.Allocations {
			line += " → " + a.InvoiceNo
		}
		lines = append(lines, line)
	}
	sum := sha256.Sum256(m.Data)
	summary := T(c.Lang, "bank_summary", len(txs), len(batch.Items), in.String(), out.String(), len(unmatched))
	var b strings.Builder
	if len(lines) > 0 {
		b.WriteString(T(c.Lang, "bank_matched") + "\n" + strings.Join(head(lines, 10), "\n"))
	}
	if len(unmatched) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(T(c.Lang, "bank_unmatched") + "\n" + strings.Join(head(unmatched, 10), "\n"))
	}
	if len(batch.Items) == 0 {
		return Outcome{Text: summary + "\n" + b.String()}, nil
	}
	return Outcome{Record: batch, Summary: summary, Text: b.String(), Fingerprint: "bank:" + hex.EncodeToString(sum[:]), Capability: "import_bank_statement"}, nil
}

func parseStatement(ctx context.Context, e *Engine, c *Call, m *domain.Media) ([]bank.Tx, error) {
	if m.MIME != "application/pdf" && !strings.HasPrefix(m.MIME, "image/") {
		return bank.Parse(m.Data, m.MIME, m.Filename)
	}
	if !e.quotaOK(ctx, c.Snap) {
		return nil, fmt.Errorf("quota")
	}
	llm, err := e.AI.LLM(ai.PurposeExtraction, c.Snap.Settings.Providers.Extraction)
	if err != nil {
		return nil, err
	}
	comp, err := llm.Complete(ctx, ai.CompletionRequest{
		System: statementPrompt, JSON: true, MaxTokens: 4000,
		Messages: []ai.Message{{Role: "user", Text: "Ekstreyi çıkar."}}, Images: []domain.Media{*m},
	})
	if err != nil {
		return nil, err
	}
	e.recordUsage(ctx, c.Snap, ai.PurposeExtraction, c.Snap.Settings.Providers.Extraction, comp.Usage)
	var raw struct {
		Transactions []struct {
			Date, Description, Amount, Reference string
		} `json:"transactions"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(comp.Text)), &raw); err != nil {
		return nil, err
	}
	// Re-use the CSV path so amounts and dates follow one parser.
	var csv strings.Builder
	csv.WriteString("Tarih;Açıklama;Tutar;Dekont\n")
	for _, t := range raw.Transactions {
		fmt.Fprintf(&csv, "%s;%s;%s;%s\n", t.Date, strings.ReplaceAll(t.Description, ";", ","), t.Amount, t.Reference)
	}
	return bank.Parse([]byte(csv.String()), "text/csv", "statement.csv")
}

// matchCustomer finds the counterparty: an invoice number in the description,
// then the customer name inside the description, then a unique open invoice
// with exactly this amount.
func matchCustomer(customers []dataprovider.Customer, allOpen, open []dataprovider.OverdueItem, tx bank.Tx) (dataprovider.Customer, bool) {
	byID := make(map[string]dataprovider.Customer, len(customers))
	for _, c := range customers {
		byID[c.ID] = c
	}
	if m := invoiceNoRe.FindStringSubmatch(tx.Description); m != nil {
		for _, inv := range allOpen {
			if strings.EqualFold(inv.InvoiceNo, m[1]) {
				return byID[inv.CustomerID], true
			}
		}
	}
	desc := fake.Fold(tx.Description)
	best, bestLen := dataprovider.Customer{}, 0
	for _, c := range customers {
		if n := fake.Fold(c.Name); len(n) > bestLen && strings.Contains(desc, n) {
			best, bestLen = c, len(n)
		}
	}
	if bestLen > 0 {
		return best, true
	}
	if tx.Amount > 0 {
		var hit *dataprovider.OverdueItem
		for i, inv := range open {
			if inv.Amount == tx.Amount {
				if hit != nil {
					return dataprovider.Customer{}, false // ambiguous
				}
				hit = &open[i]
			}
		}
		if hit != nil {
			return byID[hit.CustomerID], true
		}
	}
	return dataprovider.Customer{}, false
}

// consume reduces open invoice amounts by allocations made earlier in the same file.
func consume(open []dataprovider.OverdueItem, allocs []dataprovider.Allocation) []dataprovider.OverdueItem {
	if len(allocs) == 0 {
		return open
	}
	out := open[:0:0]
	for _, inv := range open {
		for _, a := range allocs {
			if a.InvoiceNo == inv.InvoiceNo {
				inv.Amount -= a.Amount
			}
		}
		if inv.Amount > 0 {
			out = append(out, inv)
		}
	}
	return out
}

func head(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("… +%d", len(s)-n))
	}
	return s
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
