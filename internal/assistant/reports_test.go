package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func TestPeriod(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 0, 0, 0, Istanbul) // Thursday
	from, _, key := Period(ReportWeekly, now)
	if from.Weekday() != time.Monday || key != "2026-W43" {
		t.Fatalf("weekly: %v %s", from, key)
	}
	if from, _, key := Period(ReportMonthly, now); from.Day() != 1 || key != "2026-10" {
		t.Fatalf("monthly: %v %s", from, key)
	}
	if from, to, key := Period(ReportDaily, now); from.Day() != 21 || to.Day() != 22 || key != "2026-10-21" {
		t.Fatalf("daily: %v %v %s", from, to, key)
	}
}

func TestAllocate(t *testing.T) {
	d1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	open := []dataprovider.OverdueItem{
		{InvoiceNo: "B", Amount: 30000, DueDate: d1.AddDate(0, 0, 10)},
		{InvoiceNo: "A", Amount: 50000, DueDate: d1},
		{InvoiceNo: "C", Amount: 12500, DueDate: d1.AddDate(0, 0, 20)},
	}
	if a, left := allocate(open, 12500); len(a) != 1 || a[0].InvoiceNo != "C" || left != 0 {
		t.Fatalf("exact match: %+v %d", a, left)
	}
	a, left := allocate(open, 60000)
	if len(a) != 2 || a[0].InvoiceNo != "A" || a[0].Amount != 50000 || a[1].InvoiceNo != "B" || a[1].Amount != 10000 || left != 0 {
		t.Fatalf("fifo: %+v %d", a, left)
	}
	if a, left := allocate(open, 100000); len(a) != 3 || left != 7500 {
		t.Fatalf("advance: %+v %d", a, left)
	}
}

func TestCollectionClosesInvoice(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	ctx := context.Background()
	od, _ := h.data.Overdue(ctx, "demo", 0)
	inv := od[0]
	cust, _ := h.data.FindCustomers(ctx, "demo", inv.CustomerID, 1)
	text := cust[0].Name + "'dan " + strings.TrimSuffix(inv.Amount.String(), " TL") + " TL tahsilat"
	r := h.send(t, "owner", text)
	if len(r.Drafts) != 1 || !strings.Contains(joined(r), inv.InvoiceNo+" kapanır") {
		t.Fatalf("exact allocation: %q", joined(r))
	}
	var rec dataprovider.Record
	_ = json.Unmarshal(r.Drafts[0].Payload, &rec)
	if len(rec.Allocations) != 1 || rec.Allocations[0].InvoiceNo != inv.InvoiceNo {
		t.Fatalf("allocations in payload: %+v", rec.Allocations)
	}
	after, _ := h.data.OpenInvoices(ctx, "demo", inv.CustomerID)
	for _, it := range after {
		if it.InvoiceNo == inv.InvoiceNo {
			t.Fatalf("invoice %s still open", inv.InvoiceNo)
		}
	}
}

func TestReportsOnDemand(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	h.send(t, "owner", "Ahmet Yılmaz için 10 paket A4 kağıdı sipariş oluştur") // 2.220,00 incl. 370,00 VAT
	r := h.send(t, "owner", "haftalık rapor")
	if !strings.Contains(joined(r), "Haftalık kâr-zarar") || !strings.Contains(joined(r), "2.220,00 TL") {
		t.Fatalf("weekly: %q", joined(r))
	}
	r = h.send(t, "accountant", "KDV tahmini")
	if !strings.Contains(joined(r), "Hesaplanan KDV: 370,00 TL") || !strings.Contains(joined(r), "Ödenecek KDV tahmini: 370,00 TL") {
		t.Fatalf("vat: %q", joined(r))
	}
	if r := h.send(t, "owner", "sabah özeti"); !strings.Contains(joined(r), "Günaydın") {
		t.Fatalf("daily: %q", joined(r))
	}
}
