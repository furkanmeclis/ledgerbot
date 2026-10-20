// Package fake is a deterministic data provider for development, demos, evals
// and load tests. Reference data (customers, products, invoices) is generated
// from the company id, so every replica sees the same world without storing it.
// Writes go to a Ledger (in-memory or PostgreSQL) so balances and stock move.
package fake

import (
	"context"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Entry is one committed ledger row.
type Entry struct {
	DraftID    string
	CompanyID  domain.CompanyID
	Kind       dataprovider.RecordKind
	CustomerID string
	ProductID  string
	Amount     domain.Money
	VAT        domain.Money
	Quantity   int64  // thousandths
	Ref        string // invoice number for allocations
	At         time.Time
}

// RecordAllocation is the ledger kind of an invoice allocation row.
const RecordAllocation dataprovider.RecordKind = "allocation"

// Ledger persists fake writes.
type Ledger interface {
	// Append stores entries for one draft atomically; a repeated draft id is a no-op.
	Append(ctx context.Context, draftID string, entries []Entry) (inserted bool, err error)
	// Totals returns per-customer amount sums and per-product quantity sums.
	Totals(ctx context.Context, c domain.CompanyID) (Totals, error)
	// Range returns entries of a company in [from, to).
	Range(ctx context.Context, c domain.CompanyID, from, to time.Time) ([]Entry, error)
}

// Totals aggregates ledger effects.
type Totals struct {
	CustomerDelta map[string]domain.Money // + increases what the customer owes
	ProductSold   map[string]int64        // thousandths
	Allocated     map[string]domain.Money // invoice number -> paid amount
	Cash          domain.Money            // cash effect of collections, payments and expenses
}

func newTotals() Totals {
	return Totals{CustomerDelta: map[string]domain.Money{}, ProductSold: map[string]int64{}, Allocated: map[string]domain.Money{}}
}

type world struct {
	customers []dataprovider.Customer
	opening   map[string]domain.Money
	products  []dataprovider.Product
	overdue   []dataprovider.OverdueItem
	cash      domain.Money
	recurring []recurring
}

type recurring struct {
	day    int
	amount domain.Money
	desc   string
}

// Provider is the fake driver.
type Provider struct {
	ledger Ledger
	now    func() time.Time

	mu     sync.RWMutex
	worlds map[domain.CompanyID]*world
}

// New returns a fake provider backed by ledger.
func New(ledger Ledger) *Provider {
	return &Provider{ledger: ledger, now: time.Now, worlds: make(map[domain.CompanyID]*world)}
}

var _ dataprovider.Provider = (*Provider)(nil)

var (
	firstNames = []string{"Ahmet", "Mehmet", "Ayşe", "Fatma", "Mustafa", "Zeynep", "Emre", "Elif", "Burak", "Selin", "Hakan", "Derya"}
	lastNames  = []string{"Yılmaz", "Kaya", "Demir", "Şahin", "Çelik", "Yıldız", "Aydın", "Öztürk", "Arslan", "Doğan"}
	suffixes   = []string{"Ticaret", "Gıda", "Yapı", "Otomotiv", "Tekstil", "Elektrik", "Mobilya"}
	goods      = []struct {
		name, unit string
		price      domain.Money
		vat        int
	}{
		{"A4 Fotokopi Kağıdı", "paket", 18500, 20}, {"Toner 85A", "adet", 145000, 20},
		{"Ayçiçek Yağı 5L", "adet", 42000, 1}, {"Un 50kg", "çuval", 98000, 1},
		{"Çimento 50kg", "torba", 21000, 20}, {"Vida 4x40", "kutu", 7500, 20},
		{"Kablo NYM 3x2,5", "metre", 3900, 20}, {"LED Ampul 9W", "adet", 6500, 20},
		{"Deterjan 4kg", "adet", 32000, 20}, {"Su 19L", "adet", 9000, 1},
		{"Motor Yağı 5W30", "litre", 52000, 20}, {"Balata Seti", "takım", 185000, 20},
	}
)

func (p *Provider) world(c domain.CompanyID) *world {
	p.mu.RLock()
	w, ok := p.worlds[c]
	p.mu.RUnlock()
	if ok {
		return w
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(c))
	seed := h.Sum64()
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	w = &world{opening: make(map[string]domain.Money)}
	used := map[string]bool{}
	for i := 0; len(w.customers) < 25 && i < 500; i++ {
		fn, ln := firstNames[r.IntN(len(firstNames))], lastNames[r.IntN(len(lastNames))]
		name := fn + " " + ln
		if r.IntN(3) == 0 {
			name = ln + " " + suffixes[r.IntN(len(suffixes))]
		}
		if used[name] {
			continue
		}
		used[name] = true
		id := fmt.Sprintf("C%03d", len(w.customers)+1)
		w.customers = append(w.customers, dataprovider.Customer{
			ID: id, Name: name,
			Phone: fmt.Sprintf("+90532%07d", r.IntN(10_000_000)),
		})
		w.opening[id] = domain.Money(r.IntN(200_000) * 100)
	}
	// Make sure the demo name "Ahmet Yılmaz" always exists.
	if !used["Ahmet Yılmaz"] {
		w.customers[0].Name = "Ahmet Yılmaz"
	}
	for i, g := range goods {
		w.products = append(w.products, dataprovider.Product{
			ID: fmt.Sprintf("P%03d", i+1), SKU: fmt.Sprintf("SKU-%04d", 1000+i),
			Name: g.name, Unit: g.unit, Price: g.price, VATRate: g.vat,
			Stock: int64(20+r.IntN(200)) * 1000, MinStock: 15 * 1000,
		})
	}
	now := p.now()
	for i := 0; i < 8; i++ {
		cu := w.customers[r.IntN(len(w.customers))]
		w.overdue = append(w.overdue, dataprovider.OverdueItem{
			CustomerID: cu.ID, CustomerName: cu.Name,
			InvoiceNo: fmt.Sprintf("FTR2026%06d", r.IntN(1_000_000)),
			Amount:    domain.Money((1_000 + r.IntN(50_000)) * 100),
			DueDate:   now.AddDate(0, 0, -(1 + r.IntN(60))).Truncate(24 * time.Hour),
		})
	}
	slices.SortFunc(w.overdue, func(a, b dataprovider.OverdueItem) int { return a.DueDate.Compare(b.DueDate) })
	w.cash = domain.Money((40_000 + r.IntN(160_000)) * 100)
	w.recurring = []recurring{
		{1, -domain.Money((60_000 + r.IntN(60_000)) * 100), "Personel maaşları"},
		{5, -domain.Money((15_000 + r.IntN(25_000)) * 100), "Depo kirası"},
		{12, -domain.Money((5_000 + r.IntN(10_000)) * 100), "SGK primleri"},
		{20, -domain.Money((20_000 + r.IntN(80_000)) * 100), "Tedarikçi ödemesi"},
		{26, -domain.Money((3_000 + r.IntN(7_000)) * 100), "KDV ödemesi"},
		{15, domain.Money((30_000 + r.IntN(90_000)) * 100), "Bayi çekleri tahsilatı"},
	}
	p.mu.Lock()
	if existing, ok := p.worlds[c]; ok {
		w = existing
	} else {
		p.worlds[c] = w
	}
	p.mu.Unlock()
	return w
}

// FindCustomers does a case- and diacritic-insensitive substring match.
func (p *Provider) FindCustomers(_ context.Context, c domain.CompanyID, q string, limit int) ([]dataprovider.Customer, error) {
	w := p.world(c)
	nq := Fold(q)
	var out []dataprovider.Customer
	for _, cu := range w.customers {
		if nq == "" || strings.Contains(Fold(cu.Name), nq) || cu.ID == q {
			out = append(out, cu)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (p *Provider) Balance(ctx context.Context, c domain.CompanyID, id string) (dataprovider.Balance, error) {
	w := p.world(c)
	open, ok := w.opening[id]
	if !ok {
		return dataprovider.Balance{}, dataprovider.ErrNotFound
	}
	t, err := p.ledger.Totals(ctx, c)
	if err != nil {
		return dataprovider.Balance{}, err
	}
	var overdue domain.Money
	for _, o := range w.overdue {
		if o.CustomerID == id {
			overdue += max(o.Amount-t.Allocated[o.InvoiceNo], 0)
		}
	}
	bal := open + t.CustomerDelta[id]
	return dataprovider.Balance{CustomerID: id, Balance: bal, Overdue: min(overdue, max(bal, 0)), AsOf: p.now()}, nil
}

func (p *Provider) Overdue(ctx context.Context, c domain.CompanyID, limit int) ([]dataprovider.OverdueItem, error) {
	open, err := p.open(ctx, c, "")
	if err != nil {
		return nil, err
	}
	if limit > 0 && limit < len(open) {
		open = open[:limit]
	}
	return open, nil
}

// OpenInvoices returns a customer's unpaid invoices (all seeded invoices are past due).
func (p *Provider) OpenInvoices(ctx context.Context, c domain.CompanyID, customerID string) ([]dataprovider.OverdueItem, error) {
	return p.open(ctx, c, customerID)
}

func (p *Provider) open(ctx context.Context, c domain.CompanyID, customerID string) ([]dataprovider.OverdueItem, error) {
	w := p.world(c)
	t, err := p.ledger.Totals(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]dataprovider.OverdueItem, 0, len(w.overdue))
	for _, it := range w.overdue {
		if customerID != "" && it.CustomerID != customerID {
			continue
		}
		it.Amount -= t.Allocated[it.InvoiceNo]
		if it.Amount > 0 {
			out = append(out, it)
		}
	}
	return out, nil
}

func (p *Provider) products(ctx context.Context, c domain.CompanyID) ([]dataprovider.Product, error) {
	w := p.world(c)
	t, err := p.ledger.Totals(ctx, c)
	if err != nil {
		return nil, err
	}
	out := slices.Clone(w.products)
	for i := range out {
		out[i].Stock -= t.ProductSold[out[i].ID]
	}
	return out, nil
}

func (p *Provider) FindProducts(ctx context.Context, c domain.CompanyID, q string, limit int) ([]dataprovider.Product, error) {
	all, err := p.products(ctx, c)
	if err != nil {
		return nil, err
	}
	nq := Fold(q)
	var out []dataprovider.Product
	for _, pr := range all {
		if nq == "" || strings.Contains(Fold(pr.Name), nq) || strings.EqualFold(pr.SKU, q) {
			out = append(out, pr)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (p *Provider) LowStock(ctx context.Context, c domain.CompanyID) ([]dataprovider.Product, error) {
	all, err := p.products(ctx, c)
	if err != nil {
		return nil, err
	}
	var out []dataprovider.Product
	for _, pr := range all {
		if pr.Stock < pr.MinStock {
			out = append(out, pr)
		}
	}
	return out, nil
}

func (p *Provider) DailySummary(ctx context.Context, c domain.CompanyID, day time.Time) (dataprovider.Summary, error) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	s, err := p.PeriodSummary(ctx, c, start, start.AddDate(0, 0, 1))
	s.To = time.Time{}
	return s, err
}

func (p *Provider) PeriodSummary(ctx context.Context, c domain.CompanyID, from, to time.Time) (dataprovider.Summary, error) {
	entries, err := p.ledger.Range(ctx, c, from, to)
	if err != nil {
		return dataprovider.Summary{}, err
	}
	s := dataprovider.Summary{Day: from, To: to}
	orders := map[string]bool{}
	for _, e := range entries {
		switch e.Kind {
		case dataprovider.RecordCollection:
			s.Collections += -e.Amount
		case dataprovider.RecordPayment:
			s.Payments += e.Amount
		case dataprovider.RecordExpense:
			s.Expenses += e.Amount
			s.InputVAT += e.VAT
		case dataprovider.RecordOrder:
			s.Sales += e.Amount
			s.OutputVAT += e.VAT
			orders[e.DraftID] = true
		}
	}
	s.Orders = len(orders)
	return s, nil
}

// Commit translates a record into ledger entries. Sign convention: amounts on
// customer rows increase what the customer owes.
func (p *Provider) Commit(ctx context.Context, c domain.CompanyID, r dataprovider.Record) (string, error) {
	entries, err := entriesFor(r)
	if err != nil {
		return "", err
	}
	now := p.now()
	for i := range entries {
		entries[i].DraftID, entries[i].CompanyID, entries[i].At = r.DraftID, c, now
	}
	if _, err := p.ledger.Append(ctx, r.DraftID, entries); err != nil {
		return "", err
	}
	return "FAKE-" + r.DraftID, nil
}

func entriesFor(r dataprovider.Record) ([]Entry, error) {
	var entries []Entry
	switch r.Kind {
	case dataprovider.RecordCollection:
		entries = append(entries, Entry{Kind: r.Kind, CustomerID: r.CustomerID, Amount: -r.Amount})
		for _, a := range r.Allocations {
			entries = append(entries, Entry{Kind: RecordAllocation, Ref: a.InvoiceNo, Amount: a.Amount})
		}
	case dataprovider.RecordPayment:
		entries = append(entries, Entry{Kind: r.Kind, CustomerID: r.CustomerID, Amount: r.Amount})
	case dataprovider.RecordExpense:
		entries = append(entries, Entry{Kind: r.Kind, Amount: r.Amount, VAT: r.VAT})
	case dataprovider.RecordOrder:
		for _, l := range r.Lines {
			vat := l.Total - domain.Money(int64(l.Total)*100/int64(100+l.VATRate))
			entries = append(entries, Entry{Kind: r.Kind, CustomerID: r.CustomerID, ProductID: l.ProductID, Amount: l.Total, VAT: vat, Quantity: l.Quantity})
		}
	case dataprovider.RecordCustom:
		entries = append(entries, Entry{Kind: r.Kind, CustomerID: r.CustomerID})
	case dataprovider.RecordBatch:
		for _, it := range r.Items {
			if it.Kind == dataprovider.RecordBatch {
				return nil, fmt.Errorf("fake: nested batch")
			}
			es, err := entriesFor(it)
			if err != nil {
				return nil, err
			}
			entries = append(entries, es...)
		}
	default:
		return nil, fmt.Errorf("fake: unsupported record kind %q", r.Kind)
	}
	return entries, nil
}

// CashPosition = seeded opening cash + collections − payments − expenses.
func (p *Provider) CashPosition(ctx context.Context, c domain.CompanyID) (domain.Money, error) {
	w := p.world(c)
	t, err := p.ledger.Totals(ctx, c)
	if err != nil {
		return 0, err
	}
	return w.cash + t.Cash, nil
}

// Upcoming returns the seeded recurring payables and receivables until a date,
// plus open (overdue) invoices expected within a week.
func (p *Provider) Upcoming(ctx context.Context, c domain.CompanyID, until time.Time) ([]dataprovider.Due, error) {
	w := p.world(c)
	now := p.now()
	var out []dataprovider.Due
	for _, d := range w.recurring {
		for m := 0; m < 3; m++ {
			at := time.Date(now.Year(), now.Month()+time.Month(m), d.day, 9, 0, 0, 0, now.Location())
			if at.After(now) && at.Before(until) {
				out = append(out, dataprovider.Due{Date: at, Amount: d.amount, Description: d.desc})
			}
		}
	}
	open, err := p.open(ctx, c, "")
	if err != nil {
		return nil, err
	}
	for i, it := range open {
		at := now.AddDate(0, 0, 3+i%5) // collections expected within a week
		if at.Before(until) {
			out = append(out, dataprovider.Due{Date: at, Amount: it.Amount / 2, Description: it.CustomerName + " " + it.InvoiceNo + " (beklenen %50)"})
		}
	}
	slices.SortFunc(out, func(a, b dataprovider.Due) int { return a.Date.Compare(b.Date) })
	return out, nil
}

// Fold lowercases and strips Turkish diacritics for matching.
func Fold(s string) string {
	return foldReplacer.Replace(strings.ToLower(strings.TrimSpace(s)))
}

var foldReplacer = strings.NewReplacer("ı", "i", "İ", "i", "ş", "s", "ğ", "g", "ü", "u", "ö", "o", "ç", "c", "â", "a", "î", "i", "û", "u", "i̇", "i")

// MemLedger is an in-memory Ledger for tests and single-process demos.
type MemLedger struct {
	mu      sync.Mutex
	seen    map[string]bool
	entries []Entry
}

// NewMemLedger returns an empty in-memory ledger.
func NewMemLedger() *MemLedger { return &MemLedger{seen: map[string]bool{}} }

func (m *MemLedger) Append(_ context.Context, draftID string, es []Entry) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[draftID] {
		return false, nil
	}
	m.seen[draftID] = true
	m.entries = append(m.entries, es...)
	return true, nil
}

func (m *MemLedger) Totals(_ context.Context, c domain.CompanyID) (Totals, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := newTotals()
	for _, e := range m.entries {
		if e.CompanyID != c {
			continue
		}
		if e.Kind == RecordAllocation {
			t.Allocated[e.Ref] += e.Amount
			continue
		}
		t.Cash += cashEffect(e.Kind, e.Amount)
		if e.CustomerID != "" {
			t.CustomerDelta[e.CustomerID] += e.Amount
		}
		if e.ProductID != "" {
			t.ProductSold[e.ProductID] += e.Quantity
		}
	}
	return t, nil
}

func (m *MemLedger) Range(_ context.Context, c domain.CompanyID, from, to time.Time) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.entries {
		if e.CompanyID == c && !e.At.Before(from) && e.At.Before(to) {
			out = append(out, e)
		}
	}
	return out, nil
}

// cashEffect maps a ledger row to its cash movement.
func cashEffect(k dataprovider.RecordKind, amount domain.Money) domain.Money {
	switch k {
	case dataprovider.RecordCollection:
		return -amount // stored as a decrease of what the customer owes
	case dataprovider.RecordPayment, dataprovider.RecordExpense:
		return -amount
	}
	return 0
}
