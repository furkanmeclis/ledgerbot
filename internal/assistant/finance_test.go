package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func TestBankStatementImport(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	ctx := context.Background()
	od, _ := h.data.Overdue(ctx, "demo", 0)
	inv := od[0]
	cust, _ := h.data.FindCustomers(ctx, "demo", inv.CustomerID, 1)
	csv := fmt.Sprintf("Tarih;Açıklama;Tutar\n01.10.2026;FAST GELEN %s;%s\n02.10.2026;HAVALE REF %s;1.000,00\n03.10.2026;EFT GIDEN %s;-2.500,00\n04.10.2026;BILINMEYEN ALICI;-99,90\n",
		strings.ToUpper(cust[0].Name), strings.TrimSuffix(inv.Amount.String(), " TL"), inv.InvoiceNo, cust[0].Name)
	msg := domain.InboundMessage{ID: "bank1", CompanyID: "demo", Channel: domain.ChannelDev, Sender: "owner", Kind: domain.KindDocument,
		Media: &domain.Media{MIME: "text/csv", Filename: "ekstre.csv", Data: []byte(csv)}}
	r, err := h.e.Handle(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drafts) != 1 || r.Drafts[0].Capability != "import_bank_statement" || !strings.Contains(joined(r), "4 hareket, 3 eşleşti") || !strings.Contains(joined(r), "BILINMEYEN") {
		t.Fatalf("import: %+v %q", r.Drafts, joined(r))
	}
	var batch dataprovider.Record
	_ = json.Unmarshal(r.Drafts[0].Payload, &batch)
	if len(batch.Items) != 3 || batch.Items[0].Kind != dataprovider.RecordCollection || len(batch.Items[0].Allocations) == 0 || batch.Items[2].Kind != dataprovider.RecordPayment {
		t.Fatalf("batch items: %+v", batch.Items)
	}
	// Same file again is a duplicate.
	if r, _ := h.e.Handle(ctx, msg); !strings.Contains(joined(r), "daha önce") {
		t.Fatalf("duplicate import not caught: %q", joined(r))
	}
}

func TestCashForecast(t *testing.T) {
	h := newHarness(t, nil)
	r := h.send(t, "owner", "nakit akışı nasıl")
	if !strings.Contains(joined(r), "30 gün") && !strings.Contains(joined(r), "eksiye") {
		t.Fatalf("forecast: %q", joined(r))
	}
	// Spend almost all cash: the forecast must warn.
	pos, _ := h.data.CashPosition(context.Background(), "demo")
	_, _ = h.data.Commit(context.Background(), "demo", dataprovider.Record{Kind: dataprovider.RecordExpense, Amount: pos - 100, DraftID: "big"})
	f, err := ForecastCash(context.Background(), h.data, "demo", time.Now(), 30)
	if err != nil {
		t.Fatal(err)
	}
	text, risky := CashText(f, "tr")
	if !risky || !strings.Contains(text, "eksiye") {
		t.Fatalf("expected risk: %q %+v", text, f)
	}
}

func TestAnomalyDetection(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	for _, a := range []string{"1.000", "1.100", "900"} {
		r := h.send(t, "owner", "Mehmet Çelik'e "+a+" TL ödeme yaptım")
		if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
			t.Fatalf("history %s: %q", a, joined(r))
		}
	}
	r := h.send(t, "owner", "Mehmet Çelik'e 25.000 TL ödeme yaptım")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftPending || !strings.Contains(joined(r), "Olağan dışı") {
		t.Fatalf("unusual amount must ask first: %+v %q", r.Drafts, joined(r))
	}
	r = h.send(t, "owner", "Mehmet Çelik'e 900 TL ödeme yaptım")
	if !strings.Contains(joined(r), "tekrar olabilir") {
		t.Fatalf("repeat warning: %q", joined(r))
	}
}
