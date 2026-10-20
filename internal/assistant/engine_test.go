package assistant

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

type staticTenants struct{ snap *tenant.Snapshot }

func (s staticTenants) Get(context.Context, domain.CompanyID) (*tenant.Snapshot, error) {
	return s.snap, nil
}

type harness struct {
	e      *Engine
	store  *store.Mem
	events *events.Recorder
	data   *fake.Provider
}

func newHarness(t *testing.T, mutate func(*tenant.Settings, *tenant.Matrix)) *harness {
	t.Helper()
	src := &tenant.FileSource{Dir: "../../seed/companies"}
	s, err := src.Settings(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	m, err := src.Matrix(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	s.Webhooks = []tenant.Webhook{{URL: "http://hooks.test", Secret: "x", Events: []string{"*"}}}
	if mutate != nil {
		mutate(&s, &m)
	}
	reg, err := ai.NewRegistry(ai.FakeConfig())
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{store: store.NewMem(), events: &events.Recorder{}, data: fake.New(fake.NewMemLedger())}
	h.e = &Engine{
		Tenants: staticTenants{tenant.NewSnapshot(s, m)},
		Data:    h.data, AI: reg, Store: h.store, Events: h.events,
		FX:   fx.NewConverter(fx.Static{"USD": 34.5, "EUR": 37.25}),
		Caps: NewRegistry(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return h
}

func (h *harness) send(t *testing.T, sender string, text string) Reply {
	t.Helper()
	r, err := h.e.Handle(context.Background(), domain.InboundMessage{
		ID: events.NewID("msg"), CompanyID: "demo", Channel: domain.ChannelDev, Sender: sender,
		Kind: domain.KindText, Text: text, ReceivedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func joined(r Reply) string { return strings.Join(r.Texts, "\n") }

func TestAskApprovalFlow(t *testing.T) {
	h := newHarness(t, nil)
	before, _ := h.data.Balance(context.Background(), "demo", "C001")

	r := h.send(t, "accountant", "Ahmet Yılmaz'dan 5.000 TL tahsilat")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftPending {
		t.Fatalf("expected one pending draft, got %+v / %q", r.Drafts, joined(r))
	}
	if !strings.Contains(joined(r), "5.000,00 TL") || !strings.Contains(joined(r), "evet") {
		t.Fatalf("unexpected reply %q", joined(r))
	}

	r = h.send(t, "accountant", "evet")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
		t.Fatalf("expected committed draft, got %+v / %q", r.Drafts, joined(r))
	}
	after, _ := h.data.Balance(context.Background(), "demo", "C001")
	if after.Balance != before.Balance-500000 {
		t.Fatalf("balance %d -> %d, want -500000", before.Balance, after.Balance)
	}
	if r := h.send(t, "accountant", "evet"); !strings.Contains(joined(r), "Onay bekleyen") {
		t.Fatalf("second yes should find nothing pending: %q", joined(r))
	}
	types := strings.Join(h.events.Types(), ",")
	for _, want := range []string{events.DraftCreated, events.DraftCommitted} {
		if !strings.Contains(types, want) {
			t.Errorf("missing event %s in %s", want, types)
		}
	}
}

func TestAutoSaveAndReplyOnly(t *testing.T) {
	h := newHarness(t, nil)
	// Company allows auto_save; owner has no override so it commits directly.
	r := h.send(t, "owner", "Mehmet'e 2.000 TL ödeme yaptım")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
		t.Fatalf("owner should auto-save: %+v %q", r.Drafts, joined(r))
	}
	// A role can only make the mode stricter, never looser.
	h = newHarness(t, func(s *tenant.Settings, m *tenant.Matrix) {
		s.SaveMode = domain.SaveAskApproval
		r := m.Roles["owner"]
		r.SaveMode = domain.SaveAuto
		m.Roles["owner"] = r
	})
	r = h.send(t, "owner", "Mehmet'e 2.000 TL ödeme yaptım")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftPending {
		t.Fatalf("role must not loosen save mode: %+v", r.Drafts)
	}

	h = newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	r = h.send(t, "owner", "Ahmet Yılmaz'dan 500 dolar aldım")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
		t.Fatalf("auto_save should commit: %+v %q", r.Drafts, joined(r))
	}
	if !strings.Contains(joined(r), "17.250,00 TL") || !strings.Contains(joined(r), "USD") {
		t.Fatalf("fx conversion missing: %q", joined(r))
	}

	h = newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveReplyOnly })
	r = h.send(t, "accountant", "Ahmet Yılmaz'dan 1.250,50 TL tahsilat")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftPreview || !strings.Contains(joined(r), "kayıt yapılmadı") {
		t.Fatalf("reply_only should only preview: %+v %q", r.Drafts, joined(r))
	}
}

func TestPermissions(t *testing.T) {
	h := newHarness(t, nil)
	r := h.send(t, "sales", "Ahmet Yılmaz'dan 5.000 TL tahsilat")
	if len(r.Drafts) != 0 || !strings.Contains(joined(r), "yetkiniz yok") {
		t.Fatalf("sales must not record collections: %q", joined(r))
	}
	r = h.send(t, "sales", "Ahmet Yılmaz'ın bakiyesi")
	if !strings.Contains(joined(r), "bakiyesi") {
		t.Fatalf("sales can query balance: %q", joined(r))
	}
	r = h.send(t, "stranger", "yardım")
	if !strings.Contains(joined(r), "bağlı değil") {
		t.Fatalf("unknown sender: %q", joined(r))
	}
}

func TestQueriesAndChain(t *testing.T) {
	h := newHarness(t, nil)
	for _, q := range []string{"stok toner", "kritik stok", "vadesi geçen alacaklar", "gün sonu özeti", "yardım"} {
		if r := h.send(t, "viewer", q); len(r.Texts) == 0 || strings.Contains(joined(r), "anlayamadım") {
			t.Errorf("%q -> %q", q, joined(r))
		}
	}
	r := h.send(t, "accountant", "Ahmet Yılmaz'a ekstre at ve vadesi geçenleri göster")
	if len(r.Intents) != 2 || len(r.Texts) != 2 {
		t.Fatalf("chain should give two answers: %+v", r)
	}
}

func TestOrder(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	r := h.send(t, "owner", "Ahmet Yılmaz için 10 paket A4 kağıdı sipariş oluştur")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
		t.Fatalf("order not committed: %q", joined(r))
	}
	// 10 × 185,00 = 1.850,00 + 20% VAT = 2.220,00
	if !strings.Contains(joined(r), "2.220,00 TL") {
		t.Fatalf("order total wrong: %q", joined(r))
	}
}

func TestExpenseImageAndDuplicate(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	img := domain.Media{MIME: "image/jpeg", Data: []byte(ai.FakeReceipt)}
	msg := domain.InboundMessage{ID: "m1", CompanyID: "demo", Channel: domain.ChannelDev, Sender: "owner", Kind: domain.KindImage, Media: &img}
	r, err := h.e.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted {
		t.Fatalf("expense not committed: %q", joined(r))
	}
	var rec struct {
		Lines  []json.RawMessage `json:"lines"`
		Amount int64             `json:"amount"`
	}
	_ = json.Unmarshal(r.Drafts[0].Payload, &rec)
	if len(rec.Lines) != 2 || rec.Amount != 218400 {
		t.Fatalf("lines/amount wrong: %d lines, %d", len(rec.Lines), rec.Amount)
	}
	r, _ = h.e.Handle(context.Background(), msg)
	if len(r.Drafts) != 0 || !strings.Contains(joined(r), "daha önce") {
		t.Fatalf("duplicate not detected: %q", joined(r))
	}
}

func TestEInvoiceXML(t *testing.T) {
	h := newHarness(t, nil)
	data, err := os.ReadFile("../einvoice/testdata/TemelFaturaOrnegi.xml")
	if err != nil {
		t.Fatal(err)
	}
	msg := domain.InboundMessage{ID: "m2", CompanyID: "demo", Channel: domain.ChannelDev, Sender: "accountant", Kind: domain.KindDocument,
		Media: &domain.Media{MIME: "application/xml", Filename: "fatura.xml", Data: data}}
	r, err := h.e.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Drafts) != 1 || !strings.Contains(joined(r), "e-Fatura") || !strings.Contains(joined(r), "17,88 TL") {
		t.Fatalf("e-invoice not parsed: %q", joined(r))
	}
	// e-invoice parsing must not spend AI quota.
	if n, _ := h.store.CallsSince(context.Background(), "demo", time.Time{}); n != 0 {
		t.Fatalf("expected zero AI calls, got %d", n)
	}
}

func TestVoiceNote(t *testing.T) {
	h := newHarness(t, nil)
	msg := domain.InboundMessage{ID: "m3", CompanyID: "demo", Channel: domain.ChannelDev, Sender: "accountant", Kind: domain.KindAudio,
		Media: &domain.Media{MIME: "audio/ogg", Data: []byte("Ahmet Yılmaz'ın bakiyesi")}}
	r, err := h.e.Handle(context.Background(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Texts) != 2 || !strings.Contains(r.Texts[0], "🎙️") || !strings.Contains(r.Texts[1], "bakiyesi") {
		t.Fatalf("voice flow: %q", joined(r))
	}
}

func TestDegradedNeverWrites(t *testing.T) {
	h := newHarness(t, func(s *tenant.Settings, _ *tenant.Matrix) { s.SaveMode = domain.SaveAuto })
	snap := *h.e.Tenants.(staticTenants).snap
	snap.Degraded = true
	h.e.Tenants = staticTenants{&snap}
	r := h.send(t, "owner", "Ahmet Yılmaz'dan 5.000 TL tahsilat")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftPreview {
		t.Fatalf("degraded mode must not commit: %+v", r.Drafts)
	}
}

func BenchmarkHandleQuery(b *testing.B) {
	t := &testing.T{}
	h := newHarness(t, nil)
	msg := domain.InboundMessage{CompanyID: "demo", Channel: domain.ChannelDev, Sender: "viewer", Kind: domain.KindText, Text: "stok toner"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.e.Handle(context.Background(), msg); err != nil {
			b.Fatal(err)
		}
	}
}
