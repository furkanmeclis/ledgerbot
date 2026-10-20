package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/nlu"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

// Call is the input of a capability.
type Call struct {
	Snap  *tenant.Snapshot
	Actor domain.Actor
	Msg   domain.InboundMessage
	Lang  string
	Args  map[string]string
}

// Outcome is a capability's result. Text is sent verbatim. When Record is set
// the engine treats it as a write and applies the save mode; Summary is the
// one-line description used in approval prompts.
type Outcome struct {
	Text        string
	Record      *dataprovider.Record
	Summary     string
	Fingerprint string // duplicate detection key (documents)
}

// Capability is one thing the assistant can do. Built-ins are registered by
// NewRegistry; host teams can add their own (code or webhook plugins) with
// Registry.Register.
type Capability struct {
	Name        string
	Description string
	Args        []string
	Write       bool
	Run         func(ctx context.Context, e *Engine, c *Call) (Outcome, error)
}

// Registry holds capabilities.
type Registry struct {
	mu   sync.RWMutex
	caps map[string]Capability
	docs []nlu.CapabilityDoc
}

// NewRegistry returns a registry with all built-in capabilities.
func NewRegistry() *Registry {
	r := &Registry{caps: map[string]Capability{}}
	for _, c := range builtins() {
		r.Register(c)
	}
	return r
}

// Register adds or replaces a capability.
func (r *Registry) Register(c Capability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.caps[c.Name] = c
	r.docs = r.docs[:0]
	names := make([]string, 0, len(r.caps))
	for n := range r.caps {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		cp := r.caps[n]
		r.docs = append(r.docs, nlu.CapabilityDoc{Name: cp.Name, Description: cp.Description, Args: cp.Args})
	}
}

// Get returns a capability by name.
func (r *Registry) Get(name string) (Capability, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.caps[name]
	return c, ok
}

// Docs returns capability docs for the LLM prompt and the /v1/capabilities endpoint.
func (r *Registry) Docs() []nlu.CapabilityDoc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.docs)
}

// List returns all capabilities sorted by name.
func (r *Registry) List() []Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Capability, 0, len(r.caps))
	for _, c := range r.caps {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Capability) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func builtins() []Capability {
	return []Capability{
		{Name: "record_collection", Description: "Record money received from a customer (tahsilat)", Args: []string{"customer", "amount", "currency", "method"}, Write: true, Run: runCashMove(dataprovider.RecordCollection)},
		{Name: "record_payment", Description: "Record money paid to a customer or supplier (ödeme)", Args: []string{"customer", "amount", "currency", "method"}, Write: true, Run: runCashMove(dataprovider.RecordPayment)},
		{Name: "record_expense", Description: "Record an expense or purchase invoice from a photo, PDF or e-invoice XML", Write: true, Run: runExpense},
		{Name: "create_order", Description: "Create a B2B sales order for a customer", Args: []string{"customer", "quantity", "product"}, Write: true, Run: runOrder},
		{Name: "query_balance", Description: "Show a customer's balance and overdue amount", Args: []string{"customer"}, Run: runBalance},
		{Name: "send_statement", Description: "Prepare a customer's account statement (ekstre)", Args: []string{"customer"}, Run: runStatement},
		{Name: "query_stock", Description: "Show stock level of a product", Args: []string{"product"}, Run: runStock},
		{Name: "query_low_stock", Description: "List products below minimum stock", Run: runLowStock},
		{Name: "query_overdue", Description: "List overdue receivables", Run: runOverdue},
		{Name: "daily_summary", Description: "Today's sales, collections, payments and expenses", Run: runSummary},
		{Name: "help", Description: "Explain what the assistant can do", Run: func(_ context.Context, _ *Engine, c *Call) (Outcome, error) {
			return Outcome{Text: T(c.Lang, "help")}, nil
		}},
	}
}

// resolveCustomer finds exactly one customer or returns a user-facing message.
func resolveCustomer(ctx context.Context, e *Engine, c *Call) (dataprovider.Customer, string, error) {
	q := strings.TrimSpace(c.Args["customer"])
	list, err := e.Data.FindCustomers(ctx, c.Actor.CompanyID, q, 6)
	if err != nil {
		return dataprovider.Customer{}, "", err
	}
	switch len(list) {
	case 0:
		return dataprovider.Customer{}, T(c.Lang, "customer_not_found", q), nil
	case 1:
		return list[0], "", nil
	}
	fq := fake.Fold(q)
	for _, cu := range list {
		if fake.Fold(cu.Name) == fq {
			return cu, "", nil
		}
	}
	var b strings.Builder
	for i, cu := range list {
		fmt.Fprintf(&b, "%d) %s\n", i+1, cu.Name)
	}
	return dataprovider.Customer{}, T(c.Lang, "customer_ambiguous", strings.TrimSpace(b.String())), nil
}

// amountTRY parses an amount and converts it to TRY; note explains the FX conversion.
func amountTRY(ctx context.Context, e *Engine, c *Call) (domain.Money, string, string, error) {
	raw := c.Args["amount"]
	amt, err := domain.ParseAmount(raw)
	if err != nil || amt <= 0 {
		return 0, "", T(c.Lang, "bad_amount", raw), nil
	}
	cur := fx.Normalize(c.Args["currency"])
	if cur == "TRY" {
		return amt, "", "", nil
	}
	if e.FX == nil {
		return 0, "", T(c.Lang, "fx_failed", cur), nil
	}
	try, rate, err := e.FX.ToTRY(ctx, cur, amt)
	if err != nil {
		e.Log.Warn("fx failed", "currency", cur, "err", err)
		return 0, "", T(c.Lang, "fx_failed", cur), nil
	}
	note := T(c.Lang, "fx_note", formatPlain(amt), cur, strconv.FormatFloat(rate.TRY, 'f', 4, 64), try.String())
	return try, note, "", nil
}

func formatPlain(m domain.Money) string { return strings.TrimSuffix(m.String(), " TL") }

func methodOf(c *Call) string {
	if m := strings.ToLower(c.Args["method"]); m != "" {
		return m
	}
	return "nakit"
}

func runCashMove(kind dataprovider.RecordKind) func(context.Context, *Engine, *Call) (Outcome, error) {
	return func(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
		cu, msg, err := resolveCustomer(ctx, e, c)
		if err != nil || msg != "" {
			return Outcome{Text: msg}, err
		}
		amt, note, msg, err := amountTRY(ctx, e, c)
		if err != nil || msg != "" {
			return Outcome{Text: msg}, err
		}
		key := "collection"
		if kind == dataprovider.RecordPayment {
			key = "payment"
		}
		method := methodOf(c)
		summary := T(c.Lang, key, cu.Name, amt.String(), method) + note
		rec := &dataprovider.Record{Kind: kind, CustomerID: cu.ID, Amount: amt, Method: method, Date: e.now(), Note: c.Msg.Text}
		return Outcome{Record: rec, Summary: summary}, nil
	}
}

func runOrder(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	cu, msg, err := resolveCustomer(ctx, e, c)
	if err != nil || msg != "" {
		return Outcome{Text: msg}, err
	}
	q := c.Args["product"]
	prods, err := e.Data.FindProducts(ctx, c.Actor.CompanyID, firstWord(q), 1)
	if err != nil {
		return Outcome{}, err
	}
	if len(prods) == 0 {
		return Outcome{Text: T(c.Lang, "product_not_found", q)}, nil
	}
	p := prods[0]
	qtyF, err := strconv.ParseFloat(strings.ReplaceAll(c.Args["quantity"], ",", "."), 64)
	if err != nil || qtyF <= 0 {
		return Outcome{Text: T(c.Lang, "bad_amount", c.Args["quantity"])}, nil
	}
	qty := int64(qtyF * 1000)
	net := domain.Money(int64(p.Price) * qty / 1000)
	gross := net + domain.Money(int64(net)*int64(p.VATRate)/100)
	line := dataprovider.Line{ProductID: p.ID, Name: p.Name, Quantity: qty, Unit: p.Unit, UnitPrice: p.Price, VATRate: p.VATRate, Total: gross}
	summary := T(c.Lang, "order", cu.Name, formatQty(qty)+" "+p.Unit+" "+p.Name, p.Price.String(), gross.String())
	rec := &dataprovider.Record{Kind: dataprovider.RecordOrder, CustomerID: cu.ID, Amount: gross, Lines: []dataprovider.Line{line}, Date: e.now(), Note: c.Msg.Text}
	return Outcome{Record: rec, Summary: summary}, nil
}

func firstWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return s
	}
	// "A4 kağıdı" -> "A4"; longer product names still match on the first token.
	return f[0]
}

func formatQty(q int64) string {
	if q%1000 == 0 {
		return strconv.FormatInt(q/1000, 10)
	}
	return strings.Replace(strconv.FormatFloat(float64(q)/1000, 'f', -1, 64), ".", ",", 1)
}

func runBalance(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	cu, msg, err := resolveCustomer(ctx, e, c)
	if err != nil || msg != "" {
		return Outcome{Text: msg}, err
	}
	b, err := e.Data.Balance(ctx, c.Actor.CompanyID, cu.ID)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Text: T(c.Lang, "balance", cu.Name, b.Balance.String(), b.Overdue.String())}, nil
}

func runStatement(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	cu, msg, err := resolveCustomer(ctx, e, c)
	if err != nil || msg != "" {
		return Outcome{Text: msg}, err
	}
	b, err := e.Data.Balance(ctx, c.Actor.CompanyID, cu.ID)
	if err != nil {
		return Outcome{}, err
	}
	items, err := e.Data.Overdue(ctx, c.Actor.CompanyID, 0)
	if err != nil {
		return Outcome{}, err
	}
	var lines strings.Builder
	for _, it := range items {
		if it.CustomerID == cu.ID {
			fmt.Fprintf(&lines, "• %s  %s  %s\n", it.DueDate.Format("02.01.2006"), it.InvoiceNo, it.Amount)
		}
	}
	return Outcome{Text: strings.TrimSpace(T(c.Lang, "statement", cu.Name, b.Balance.String(), b.Overdue.String(), lines.String()))}, nil
}

func runStock(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	q := c.Args["product"]
	prods, err := e.Data.FindProducts(ctx, c.Actor.CompanyID, q, 5)
	if err != nil {
		return Outcome{}, err
	}
	if len(prods) == 0 {
		prods, err = e.Data.FindProducts(ctx, c.Actor.CompanyID, firstWord(q), 5)
		if err != nil {
			return Outcome{}, err
		}
	}
	if len(prods) == 0 {
		return Outcome{Text: T(c.Lang, "product_not_found", q)}, nil
	}
	var b strings.Builder
	for i, p := range prods {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(T(c.Lang, "stock", p.Name, formatQty(p.Stock), p.Unit, formatQty(p.MinStock)))
	}
	return Outcome{Text: b.String()}, nil
}

func runLowStock(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	prods, err := e.Data.LowStock(ctx, c.Actor.CompanyID)
	if err != nil {
		return Outcome{}, err
	}
	if len(prods) == 0 {
		return Outcome{Text: T(c.Lang, "low_stock_none")}, nil
	}
	var b strings.Builder
	for _, p := range prods {
		fmt.Fprintf(&b, "• %s: %s %s\n", p.Name, formatQty(p.Stock), p.Unit)
	}
	return Outcome{Text: T(c.Lang, "low_stock_list", strings.TrimSpace(b.String()))}, nil
}

func runOverdue(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	items, err := e.Data.Overdue(ctx, c.Actor.CompanyID, 10)
	if err != nil {
		return Outcome{}, err
	}
	if len(items) == 0 {
		return Outcome{Text: T(c.Lang, "overdue_none")}, nil
	}
	var total domain.Money
	var b strings.Builder
	now := e.now()
	for _, it := range items {
		total += it.Amount
		days := int(now.Sub(it.DueDate).Hours() / 24)
		fmt.Fprintf(&b, "• %s: %s (%d gün)\n", it.CustomerName, it.Amount, days)
	}
	return Outcome{Text: T(c.Lang, "overdue_list", total.String(), strings.TrimSpace(b.String()))}, nil
}

func runSummary(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	day := e.now()
	s, err := e.Data.DailySummary(ctx, c.Actor.CompanyID, day)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Text: T(c.Lang, "summary", day.Format("02.01.2006"), s.Sales.String(), s.Orders, s.Collections.String(), s.Payments.String(), s.Expenses.String())}, nil
}

// fingerprint hashes the parts that identify a document.
func fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strings.ToLower(strings.TrimSpace(p))))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func dateOr(s string, fallback time.Time) time.Time {
	for _, layout := range []string{"2006-01-02", "02.01.2006", "02/01/2006", "2.1.2006"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t
		}
	}
	return fallback
}
