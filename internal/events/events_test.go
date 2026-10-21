package events

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

var idRe = regexp.MustCompile(`^[a-z]+_[0-9a-f]{32}$`)

func TestNewID(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 1000 {
		id := NewID("evt")
		if !idRe.MatchString(id) || !strings.HasPrefix(id, "evt_") {
			t.Fatalf("bad id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	if id := NewID(""); !regexp.MustCompile(`^_[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("empty prefix id %q", id)
	}
}

func TestNew(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC()
	e, err := New("acme", StockLow, "simulator", StockLowData{ProductID: "P001", Name: "Toner", Stock: 5000, MinStock: 15000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(e.ID, "evt_") || e.Type != StockLow || e.CompanyID != "acme" || e.Source != "simulator" {
		t.Fatalf("unexpected envelope %+v", e)
	}
	if e.OccurredAt.Location() != time.UTC || e.OccurredAt.Before(before.Add(-time.Second)) || e.OccurredAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("OccurredAt = %v", e.OccurredAt)
	}
	if want := `{"product_id":"P001","name":"Toner","stock":5000,"min_stock":15000}`; string(e.Data) != want {
		t.Fatalf("Data = %s, want %s", e.Data, want)
	}
}

func TestNewPayloads(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data any
		want string
	}{
		{"nil", nil, "null"},
		{"map", map[string]int{"a": 1}, `{"a":1}`},
		{"raw", json.RawMessage(`{"x":true}`), `{"x":true}`},
		{"overdue", InvoiceOverdueData{CustomerID: "C001", CustomerName: "Ahmet", InvoiceNo: "F1", Amount: domain.Money(125050), DueDate: "2026-10-01"},
			`{"customer_id":"C001","customer_name":"Ahmet","invoice_no":"F1","amount":125050,"due_date":"2026-10-01"}`},
		{"payment no ref", PaymentReceivedData{CustomerID: "C1", Amount: 100}, `{"customer_id":"C1","amount":100}`},
		{"stock with unit", StockLowData{ProductID: "P", Name: "Un", Unit: "çuval"}, `{"product_id":"P","name":"Un","unit":"çuval","stock":0,"min_stock":0}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			e, err := New("c", OrderCreated, "host", c.data)
			if err != nil {
				t.Fatal(err)
			}
			if string(e.Data) != c.want {
				t.Fatalf("Data = %s, want %s", e.Data, c.want)
			}
		})
	}
}

func TestNewUnmarshalableData(t *testing.T) {
	t.Parallel()
	for _, bad := range []any{make(chan int), func() {}, map[bool]int{true: 1}} {
		if _, err := New("c", AlertSent, "ledgerbot", bad); err == nil {
			t.Fatalf("New(%T) expected error", bad)
		}
	}
}

func TestEventJSONRoundTrip(t *testing.T) {
	t.Parallel()
	e, err := New("acme", PaymentReceived, "host", PaymentReceivedData{CustomerID: "C1", Amount: 50000, Reference: "R1"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"id"`, `"type"`, `"company_id"`, `"source"`, `"occurred_at"`, `"data"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
	var back Event
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != e.ID || back.Type != e.Type || back.CompanyID != e.CompanyID || !back.OccurredAt.Equal(e.OccurredAt) || string(back.Data) != string(e.Data) {
		t.Fatalf("round trip mismatch: %+v vs %+v", back, e)
	}
	var d PaymentReceivedData
	if err := json.Unmarshal(back.Data, &d); err != nil || d.Amount != 50000 || d.Reference != "R1" {
		t.Fatalf("payload = %+v, %v", d, err)
	}
}

func TestEventTypesUnique(t *testing.T) {
	t.Parallel()
	all := []string{StockLow, InvoiceOverdue, PaymentReceived, OrderCreated, SettingsChanged,
		DraftCreated, DraftCommitted, DraftRejected, DraftDuplicate, MessageHandled, AlertSent, DeletionDone, AnomalyDetected}
	seen := map[string]bool{}
	re := regexp.MustCompile(`^[a-z]+\.[a-z]+$`)
	for _, typ := range all {
		if seen[typ] || !re.MatchString(typ) {
			t.Fatalf("bad or duplicate event type %q", typ)
		}
		seen[typ] = true
	}
}

func TestRecorder(t *testing.T) {
	t.Parallel()
	var r Recorder
	var _ Publisher = &r
	if got := r.Types(); len(got) != 0 {
		t.Fatalf("empty recorder types = %v", got)
	}
	for _, typ := range []string{DraftCreated, DraftCommitted, AlertSent} {
		if err := r.Publish(context.Background(), Event{Type: typ}); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := r.Types(), []string{DraftCreated, DraftCommitted, AlertSent}; !slices.Equal(got, want) {
		t.Fatalf("Types = %v, want %v", got, want)
	}
}

func TestRecorderConcurrent(t *testing.T) {
	t.Parallel()
	var r Recorder
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 50 {
				_ = r.Publish(context.Background(), Event{Type: MessageHandled})
				_ = r.Types()
			}
		})
	}
	wg.Wait()
	if n := len(r.Types()); n != 1000 {
		t.Fatalf("recorded %d events, want 1000", n)
	}
}

func BenchmarkNew(b *testing.B) {
	d := StockLowData{ProductID: "P001", Name: "Toner", Stock: 5000, MinStock: 15000}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = New("acme", StockLow, "simulator", d)
	}
}
