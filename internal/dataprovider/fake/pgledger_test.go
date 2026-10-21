package fake

import (
	"strconv"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
)

// TestPGLedgerMatchesMemLedger runs the same writes through both ledgers and
// compares the results. It skips without LEDGERBOT_TEST_DATABASE_URL.
func TestPGLedgerMatchesMemLedger(t *testing.T) {
	pg := PGLedger{DB: testdb.Open(t)}
	mem := NewMemLedger()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	c := domain.CompanyID("pgtest-" + run)
	at := time.Now().UTC().Truncate(time.Microsecond)
	batches := map[string][]Entry{
		run + "-1": {
			{DraftID: run + "-1", CompanyID: c, Kind: dataprovider.RecordCollection, CustomerID: "C1", Amount: -5000, At: at},
			{DraftID: run + "-1", CompanyID: c, Kind: RecordAllocation, Ref: "F1", Amount: 5000, At: at},
		},
		run + "-2": {
			{DraftID: run + "-2", CompanyID: c, Kind: dataprovider.RecordOrder, CustomerID: "C2", ProductID: "P1", Amount: 1200, VAT: 200, Quantity: 2000, At: at.Add(time.Minute)},
		},
		run + "-3": {
			{DraftID: run + "-3", CompanyID: c, Kind: dataprovider.RecordExpense, Amount: 700, VAT: 100, At: at.Add(2 * time.Minute)},
		},
	}
	for id, es := range batches {
		for _, l := range []Ledger{pg, mem} {
			ok, err := l.Append(ctx, id, es)
			if err != nil || !ok {
				t.Fatalf("%T Append(%s) = %v, %v", l, id, ok, err)
			}
			if ok, err := l.Append(ctx, id, es); err != nil || ok {
				t.Fatalf("%T duplicate Append(%s) = %v, %v", l, id, ok, err)
			}
		}
	}
	tp, err := pg.Totals(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	tm, _ := mem.Totals(ctx, c)
	if tp.Cash != tm.Cash || tp.Allocated["F1"] != tm.Allocated["F1"] || tp.CustomerDelta["C1"] != tm.CustomerDelta["C1"] ||
		tp.CustomerDelta["C2"] != tm.CustomerDelta["C2"] || tp.ProductSold["P1"] != tm.ProductSold["P1"] {
		t.Fatalf("totals differ:\n pg  %+v\n mem %+v", tp, tm)
	}
	rp, err := pg.Range(ctx, c, at, at.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	rm, _ := mem.Range(ctx, c, at, at.Add(2*time.Minute))
	if len(rp) != len(rm) || len(rp) != 3 {
		t.Fatalf("range pg=%d mem=%d, want 3", len(rp), len(rm))
	}
}
