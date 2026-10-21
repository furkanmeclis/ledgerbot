package fake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

var fixedNow = time.Date(2026, 10, 21, 14, 30, 0, 0, time.UTC)

func newTestProvider() *Provider {
	p := New(NewMemLedger())
	p.now = func() time.Time { return fixedNow }
	return p
}

var ctx = context.Background()

func TestFold(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Ahmet Yılmaz":      "ahmet yilmaz",
		"  ŞAHİN  ":         "sahin",
		"İSTANBUL":          "istanbul",
		"Çelik Öztürk":      "celik ozturk",
		"Doğan Gıda":        "dogan gida",
		"Kâğıt":             "kagit",
		"Îmâr Ûrün":         "imar urun",
		"ABC 123":           "abc 123",
		"":                  "",
		"Ayçiçek Yağı 5L":   "aycicek yagi 5l",
		"KABLO NYM 3x2,5":   "kablo nym 3x2,5",
		"i̇stanbul":         "istanbul",
		"Gül Şükrü Ünal":    "gul sukru unal",
		"ÖZTÜRK ÇİÇEKÇİLİK": "ozturk cicekcilik",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorldIsDeterministic(t *testing.T) {
	t.Parallel()
	a, b := newTestProvider(), newTestProvider()
	ca, _ := a.FindCustomers(ctx, "acme", "", 0)
	cb, _ := b.FindCustomers(ctx, "acme", "", 0)
	if !slices.Equal(ca, cb) {
		t.Fatal("same company produced different customers")
	}
	other, _ := a.FindCustomers(ctx, "globex", "", 0)
	if slices.Equal(ca, other) {
		t.Fatal("different companies produced identical customers")
	}
	if len(ca) != 25 {
		t.Fatalf("customers = %d, want 25", len(ca))
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for i, c := range ca {
		if names[c.Name] || ids[c.ID] {
			t.Fatalf("duplicate customer %+v", c)
		}
		names[c.Name], ids[c.ID] = true, true
		if !strings.HasPrefix(c.Phone, "+90532") || len(c.Phone) != 13 {
			t.Fatalf("bad phone %q", c.Phone)
		}
		if i == 0 && c.ID != "C001" {
			t.Fatalf("first id %s", c.ID)
		}
	}
	oa, _ := a.Overdue(ctx, "acme", 0)
	ob, _ := b.Overdue(ctx, "acme", 0)
	if len(oa) != 8 || !slices.Equal(oa, ob) {
		t.Fatalf("overdue not deterministic: %d vs %d", len(oa), len(ob))
	}
}

func TestDemoCustomerAlwaysExists(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	for _, c := range []domain.CompanyID{"acme", "globex", "initech", "c-1", "c-2", "c-3", "", "şirket"} {
		got, err := p.FindCustomers(ctx, c, "ahmet yilmaz", 0)
		if err != nil || len(got) == 0 {
			t.Fatalf("%q: Ahmet Yılmaz missing (%v)", c, err)
		}
	}
}

func TestFindCustomers(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	all, _ := p.FindCustomers(ctx, "acme", "", 0)
	if got, _ := p.FindCustomers(ctx, "acme", "", 3); len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	if got, _ := p.FindCustomers(ctx, "acme", "AHMET YILMAZ", 0); len(got) == 0 {
		t.Fatal("case-insensitive match failed")
	}
	if got, _ := p.FindCustomers(ctx, "acme", "Ahmet Yilmaz", 0); len(got) == 0 {
		t.Fatal("diacritic-insensitive match failed")
	}
	if got, _ := p.FindCustomers(ctx, "acme", all[3].ID, 0); len(got) != 1 || got[0].ID != all[3].ID {
		t.Fatalf("id lookup = %+v", got)
	}
	if got, _ := p.FindCustomers(ctx, "acme", "zzzz-nobody", 0); got != nil {
		t.Fatalf("no match should be nil, got %+v", got)
	}
}

func TestProducts(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	all, err := p.FindProducts(ctx, "acme", "", 0)
	if err != nil || len(all) != len(goods) {
		t.Fatalf("products = %d, %v", len(all), err)
	}
	for i, pr := range all {
		if pr.Name != goods[i].name || pr.Price != goods[i].price || pr.VATRate != goods[i].vat || pr.MinStock != 15000 || pr.Stock < 20000 || pr.Stock >= 220000 {
			t.Fatalf("product %d = %+v", i, pr)
		}
	}
	cases := []struct {
		q    string
		want string
	}{
		{"toner", "P002"}, {"TONER 85a", "P002"}, {"kagidi", "P001"}, {"ayçiçek", "P003"}, {"sku-1004", "P005"}, {"SKU-1011", "P012"},
	}
	for _, c := range cases {
		got, _ := p.FindProducts(ctx, "acme", c.q, 1)
		if len(got) != 1 || got[0].ID != c.want {
			t.Errorf("FindProducts(%q) = %+v, want %s", c.q, got, c.want)
		}
	}
	if low, _ := p.LowStock(ctx, "acme"); len(low) != 0 {
		t.Fatalf("seeded stock should be above minimum, low = %+v", low)
	}
}

func TestBalanceAndCollection(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	overdue, _ := p.Overdue(ctx, "acme", 0)
	inv := overdue[0]
	before, err := p.Balance(ctx, "acme", inv.CustomerID)
	if err != nil {
		t.Fatal(err)
	}
	if before.AsOf != fixedNow || before.CustomerID != inv.CustomerID || before.Overdue > max(before.Balance, 0) {
		t.Fatalf("balance = %+v", before)
	}
	open, _ := p.OpenInvoices(ctx, "acme", inv.CustomerID)
	if len(open) == 0 {
		t.Fatal("customer has no open invoices")
	}
	for _, o := range open {
		if o.CustomerID != inv.CustomerID {
			t.Fatalf("foreign invoice %+v", o)
		}
	}
	ext, err := p.Commit(ctx, "acme", dataprovider.Record{
		Kind: dataprovider.RecordCollection, CustomerID: inv.CustomerID, Amount: inv.Amount, DraftID: "d-col",
		Allocations: []dataprovider.Allocation{{InvoiceNo: inv.InvoiceNo, Amount: inv.Amount}},
	})
	if err != nil || ext != "FAKE-d-col" {
		t.Fatalf("Commit = %q, %v", ext, err)
	}
	after, _ := p.Balance(ctx, "acme", inv.CustomerID)
	if after.Balance != before.Balance-inv.Amount {
		t.Fatalf("balance %d -> %d, want -%d", before.Balance, after.Balance, inv.Amount)
	}
	for _, o := range mustOverdue(t, p) {
		if o.InvoiceNo == inv.InvoiceNo {
			t.Fatalf("fully allocated invoice still open: %+v", o)
		}
	}
	// Idempotent on draft id.
	if _, err := p.Commit(ctx, "acme", dataprovider.Record{Kind: dataprovider.RecordCollection, CustomerID: inv.CustomerID, Amount: inv.Amount, DraftID: "d-col"}); err != nil {
		t.Fatal(err)
	}
	if again, _ := p.Balance(ctx, "acme", inv.CustomerID); again.Balance != after.Balance {
		t.Fatal("repeated draft changed the balance")
	}
	if _, err := p.Balance(ctx, "acme", "C999"); !errors.Is(err, dataprovider.ErrNotFound) {
		t.Fatalf("unknown customer err = %v", err)
	}
	// Other companies are unaffected.
	if o, _ := p.Overdue(ctx, "globex", 0); len(o) != 8 {
		t.Fatalf("globex overdue = %d", len(o))
	}
}

func mustOverdue(t *testing.T, p *Provider) []dataprovider.OverdueItem {
	t.Helper()
	o, err := p.Overdue(ctx, "acme", 0)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPartialAllocation(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	inv := mustOverdue(t, p)[0]
	half := inv.Amount / 2
	if _, err := p.Commit(ctx, "acme", dataprovider.Record{Kind: dataprovider.RecordCollection, CustomerID: inv.CustomerID, Amount: half, DraftID: "d-half",
		Allocations: []dataprovider.Allocation{{InvoiceNo: inv.InvoiceNo, Amount: half}}}); err != nil {
		t.Fatal(err)
	}
	for _, o := range mustOverdue(t, p) {
		if o.InvoiceNo == inv.InvoiceNo {
			if o.Amount != inv.Amount-half {
				t.Fatalf("remaining = %d, want %d", o.Amount, inv.Amount-half)
			}
			return
		}
	}
	t.Fatal("partially paid invoice disappeared")
}

func TestOverdueSortedAndLimited(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	all := mustOverdue(t, p)
	if !slices.IsSortedFunc(all, func(a, b dataprovider.OverdueItem) int { return a.DueDate.Compare(b.DueDate) }) {
		t.Fatal("overdue not sorted by due date")
	}
	for _, o := range all {
		if !o.DueDate.Before(fixedNow) || o.Amount <= 0 || !strings.HasPrefix(o.InvoiceNo, "FTR2026") {
			t.Fatalf("bad overdue item %+v", o)
		}
	}
	for _, lim := range []int{0, 1, 3, 8, 100} {
		got, _ := p.Overdue(ctx, "acme", lim)
		want := len(all)
		if lim > 0 && lim < want {
			want = lim
		}
		if len(got) != want {
			t.Fatalf("limit %d returned %d", lim, len(got))
		}
	}
}

func TestOrderMovesStockAndVAT(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	prods, _ := p.FindProducts(ctx, "acme", "toner", 1)
	toner := prods[0]
	qty := toner.Stock - 1000 // leave 1 unit, below the 15 unit minimum
	rec := dataprovider.Record{Kind: dataprovider.RecordOrder, CustomerID: "C001", DraftID: "d-ord", Lines: []dataprovider.Line{
		{ProductID: toner.ID, Quantity: qty, VATRate: 20, Total: 120000},
		{ProductID: "P003", Quantity: 2000, VATRate: 1, Total: 10100},
	}}
	if _, err := p.Commit(ctx, "acme", rec); err != nil {
		t.Fatal(err)
	}
	got, _ := p.FindProducts(ctx, "acme", toner.SKU, 1)
	if got[0].Stock != 1000 {
		t.Fatalf("stock = %d, want 1000", got[0].Stock)
	}
	low, _ := p.LowStock(ctx, "acme")
	if len(low) != 1 || low[0].ID != toner.ID {
		t.Fatalf("low stock = %+v", low)
	}
	s, err := p.DailySummary(ctx, "acme", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if s.Sales != 130100 || s.OutputVAT != 20000+100 || s.Orders != 1 || !s.To.IsZero() || !s.Day.Equal(time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("summary = %+v", s)
	}
}

func TestPeriodSummaryAndCash(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	cash0, _ := p.CashPosition(ctx, "acme")
	recs := []dataprovider.Record{
		{Kind: dataprovider.RecordCollection, CustomerID: "C001", Amount: 50000, DraftID: "a"},
		{Kind: dataprovider.RecordPayment, CustomerID: "C002", Amount: 20000, DraftID: "b"},
		{Kind: dataprovider.RecordExpense, Amount: 12000, VAT: 2000, DraftID: "c"},
		{Kind: dataprovider.RecordCustom, CustomerID: "C003", DraftID: "d"},
		{Kind: dataprovider.RecordBatch, DraftID: "e", Items: []dataprovider.Record{
			{Kind: dataprovider.RecordCollection, CustomerID: "C004", Amount: 1000},
			{Kind: dataprovider.RecordExpense, Amount: 600, VAT: 100},
		}},
	}
	for _, r := range recs {
		if _, err := p.Commit(ctx, "acme", r); err != nil {
			t.Fatalf("%s: %v", r.Kind, err)
		}
	}
	from := fixedNow.Add(-time.Hour)
	s, err := p.PeriodSummary(ctx, "acme", from, fixedNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if s.Collections != 51000 || s.Payments != 20000 || s.Expenses != 12600 || s.InputVAT != 2100 || s.Orders != 0 || !s.Day.Equal(from) {
		t.Fatalf("summary = %+v", s)
	}
	if empty, _ := p.PeriodSummary(ctx, "acme", fixedNow.Add(time.Hour), fixedNow.Add(2*time.Hour)); empty.Collections != 0 {
		t.Fatalf("window excludes entries, got %+v", empty)
	}
	if empty, _ := p.PeriodSummary(ctx, "acme", from, fixedNow); empty.Collections != 0 {
		t.Fatal("range end must be exclusive")
	}
	cash, _ := p.CashPosition(ctx, "acme")
	if want := cash0 + 51000 - 20000 - 12600; cash != want {
		t.Fatalf("cash = %d, want %d", cash, want)
	}
	if b, _ := p.Balance(ctx, "acme", "C002"); b.Balance <= 0 {
		t.Fatalf("payment should raise C002 balance: %+v", b)
	}
}

func TestCommitErrors(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	cases := []dataprovider.Record{
		{Kind: "refund", DraftID: "x1"},
		{Kind: "", DraftID: "x2"},
		{Kind: dataprovider.RecordBatch, DraftID: "x3", Items: []dataprovider.Record{{Kind: dataprovider.RecordBatch}}},
		{Kind: dataprovider.RecordBatch, DraftID: "x4", Items: []dataprovider.Record{{Kind: dataprovider.RecordExpense}, {Kind: "bogus"}}},
	}
	for _, r := range cases {
		if _, err := p.Commit(ctx, "acme", r); err == nil {
			t.Errorf("Commit(%+v) expected error", r)
		}
	}
	if s, _ := p.DailySummary(ctx, "acme", fixedNow); s.Expenses != 0 {
		t.Fatal("failed batch wrote partial entries")
	}
}

func TestEntriesForOrderVAT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		total domain.Money
		rate  int
		vat   domain.Money
	}{
		{12000, 20, 2000}, {10100, 1, 100}, {11000, 10, 1000}, {5000, 0, 0}, {1, 20, 1}, {99, 20, 17},
	}
	for _, c := range cases {
		es, err := entriesFor(dataprovider.Record{Kind: dataprovider.RecordOrder, Lines: []dataprovider.Line{{Total: c.total, VATRate: c.rate}}})
		if err != nil || len(es) != 1 || es[0].VAT != c.vat || es[0].Amount != c.total {
			t.Errorf("order %d @%d%%: %+v, %v; want vat %d", c.total, c.rate, es, err, c.vat)
		}
	}
}

func TestUpcoming(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	until := fixedNow.AddDate(0, 1, 0)
	due, err := p.Upcoming(ctx, "acme", until)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) == 0 {
		t.Fatal("no upcoming items")
	}
	if !slices.IsSortedFunc(due, func(a, b dataprovider.Due) int { return a.Date.Compare(b.Date) }) {
		t.Fatal("upcoming not sorted")
	}
	var expected, recurringIn, recurringOut int
	for _, d := range due {
		if !d.Date.After(fixedNow) || !d.Date.Before(until) {
			t.Fatalf("out of window %+v", d)
		}
		switch {
		case strings.HasSuffix(d.Description, "(beklenen %50)"):
			expected++
			if d.Amount <= 0 {
				t.Fatalf("expected collection must be positive: %+v", d)
			}
		case d.Amount > 0:
			recurringIn++
		default:
			recurringOut++
		}
	}
	if expected != 8 || recurringIn == 0 || recurringOut == 0 {
		t.Fatalf("expected=%d in=%d out=%d", expected, recurringIn, recurringOut)
	}
	none, _ := p.Upcoming(ctx, "acme", fixedNow)
	if len(none) != 0 {
		t.Fatalf("until=now should be empty, got %d", len(none))
	}
}

func TestMemLedger(t *testing.T) {
	t.Parallel()
	l := NewMemLedger()
	t0 := fixedNow
	ok, err := l.Append(ctx, "d1", []Entry{
		{CompanyID: "a", Kind: dataprovider.RecordCollection, CustomerID: "C1", Amount: -500, At: t0},
		{CompanyID: "a", Kind: RecordAllocation, Ref: "F1", Amount: 500, At: t0},
		{CompanyID: "b", Kind: dataprovider.RecordOrder, CustomerID: "C1", ProductID: "P1", Quantity: 3000, Amount: 900, At: t0.Add(time.Hour)},
	})
	if !ok || err != nil {
		t.Fatalf("Append = %v, %v", ok, err)
	}
	if ok, _ := l.Append(ctx, "d1", []Entry{{CompanyID: "a", Amount: 1}}); ok {
		t.Fatal("duplicate draft inserted")
	}
	if ok, _ := l.Append(ctx, "empty", nil); !ok {
		t.Fatal("empty draft should still be recorded")
	}
	ta, _ := l.Totals(ctx, "a")
	if ta.CustomerDelta["C1"] != -500 || ta.Allocated["F1"] != 500 || ta.Cash != 500 || len(ta.ProductSold) != 0 {
		t.Fatalf("totals a = %+v", ta)
	}
	tb, _ := l.Totals(ctx, "b")
	if tb.ProductSold["P1"] != 3000 || tb.CustomerDelta["C1"] != 900 || tb.Cash != 0 {
		t.Fatalf("totals b = %+v", tb)
	}
	if tc, _ := l.Totals(ctx, "c"); tc.CustomerDelta == nil || tc.ProductSold == nil || tc.Allocated == nil {
		t.Fatal("totals maps must be non-nil")
	}
	if r, _ := l.Range(ctx, "a", t0, t0.Add(time.Nanosecond)); len(r) != 2 {
		t.Fatalf("range start inclusive: %d", len(r))
	}
	if r, _ := l.Range(ctx, "b", t0, t0.Add(time.Hour)); len(r) != 0 {
		t.Fatalf("range end exclusive: %d", len(r))
	}
}

func TestCashEffect(t *testing.T) {
	t.Parallel()
	cases := []struct {
		k    dataprovider.RecordKind
		amt  domain.Money
		want domain.Money
	}{
		{dataprovider.RecordCollection, -100, 100},
		{dataprovider.RecordPayment, 100, -100},
		{dataprovider.RecordExpense, 100, -100},
		{dataprovider.RecordOrder, 100, 0},
		{dataprovider.RecordCustom, 100, 0},
		{RecordAllocation, 100, 0},
	}
	for _, c := range cases {
		if got := cashEffect(c.k, c.amt); got != c.want {
			t.Errorf("cashEffect(%s, %d) = %d, want %d", c.k, c.amt, got, c.want)
		}
	}
}

type failingLedger struct{ err error }

func (f failingLedger) Append(context.Context, string, []Entry) (bool, error) { return false, f.err }
func (f failingLedger) Totals(context.Context, domain.CompanyID) (Totals, error) {
	return Totals{}, f.err
}
func (f failingLedger) Range(context.Context, domain.CompanyID, time.Time, time.Time) ([]Entry, error) {
	return nil, f.err
}

func TestLedgerErrorsPropagate(t *testing.T) {
	t.Parallel()
	boom := errors.New("db down")
	p := New(failingLedger{boom})
	calls := map[string]func() error{
		"Balance":       func() error { _, err := p.Balance(ctx, "a", "C001"); return err },
		"Overdue":       func() error { _, err := p.Overdue(ctx, "a", 0); return err },
		"OpenInvoices":  func() error { _, err := p.OpenInvoices(ctx, "a", "C001"); return err },
		"FindProducts":  func() error { _, err := p.FindProducts(ctx, "a", "", 0); return err },
		"LowStock":      func() error { _, err := p.LowStock(ctx, "a"); return err },
		"DailySummary":  func() error { _, err := p.DailySummary(ctx, "a", fixedNow); return err },
		"PeriodSummary": func() error { _, err := p.PeriodSummary(ctx, "a", fixedNow, fixedNow); return err },
		"CashPosition":  func() error { _, err := p.CashPosition(ctx, "a"); return err },
		"Upcoming":      func() error { _, err := p.Upcoming(ctx, "a", fixedNow.AddDate(0, 1, 0)); return err },
		"Commit": func() error {
			_, err := p.Commit(ctx, "a", dataprovider.Record{Kind: dataprovider.RecordExpense, DraftID: "x"})
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s err = %v, want ledger error", name, err)
		}
	}
	if _, err := p.FindCustomers(ctx, "a", "", 0); err != nil {
		t.Fatalf("FindCustomers does not touch the ledger: %v", err)
	}
}

func TestConcurrentWorldAndCommits(t *testing.T) {
	t.Parallel()
	p := newTestProvider()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			c := domain.CompanyID([]string{"a", "b", "c", "d"}[i%4])
			for j := range 20 {
				_, _ = p.FindCustomers(ctx, c, "a", 5)
				_, _ = p.Commit(ctx, c, dataprovider.Record{Kind: dataprovider.RecordExpense, Amount: 1, DraftID: string(c) + "-" + string(rune('a'+i)) + "-" + string(rune('a'+j))})
				_, _ = p.CashPosition(ctx, c)
			}
		})
	}
	wg.Wait()
	s, _ := p.DailySummary(ctx, "a", fixedNow)
	if s.Expenses != 4*20 {
		t.Fatalf("expenses = %d, want 80", s.Expenses)
	}
}

func BenchmarkFindCustomers(b *testing.B) {
	p := New(NewMemLedger())
	_, _ = p.FindCustomers(ctx, "acme", "", 0)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = p.FindCustomers(ctx, "acme", "ahmet", 5)
	}
}

func BenchmarkFold(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = Fold("ÖZTÜRK ÇİÇEKÇİLİK Gıda Ticaret")
	}
}
