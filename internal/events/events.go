// Package events defines the event contract shared by the host system, the
// fake simulator and ledgerbot itself (docs/contracts/event.schema.json).
//
// Inbound events (stock.low, invoice.overdue, payment.received ...) arrive from
// the host via POST /v1/events or from the simulator. Outbound events
// (draft.created, draft.committed ...) are emitted by ledgerbot and delivered to
// subscribed webhooks. Both directions use the same envelope.
package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Well-known event types.
const (
	// Inbound (host or simulator).
	StockLow        = "stock.low"
	InvoiceOverdue  = "invoice.overdue"
	PaymentReceived = "payment.received"
	OrderCreated    = "order.created"
	SettingsChanged = "settings.changed"

	// Outbound (ledgerbot).
	DraftCreated   = "draft.created"
	DraftCommitted = "draft.committed"
	DraftRejected  = "draft.rejected"
	DraftDuplicate = "draft.duplicate"
	MessageHandled = "message.handled"
	AlertSent      = "alert.sent"
	DeletionDone   = "deletion.completed"
)

// Event is the envelope.
type Event struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	CompanyID  domain.CompanyID `json:"company_id"`
	Source     string           `json:"source"` // "host", "simulator", "ledgerbot"
	OccurredAt time.Time        `json:"occurred_at"`
	Data       json.RawMessage  `json:"data"`
}

// New builds an event with a random id.
func New(company domain.CompanyID, typ, source string, data any) (Event, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return Event{}, err
	}
	return Event{ID: NewID("evt"), Type: typ, CompanyID: company, Source: source, OccurredAt: time.Now().UTC(), Data: raw}, nil
}

// NewID returns a prefixed 128-bit random id.
func NewID(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Publisher accepts events for asynchronous processing.
type Publisher interface {
	Publish(ctx context.Context, e Event) error
}

// Recorder is an in-memory Publisher for tests.
type Recorder struct {
	mu     sync.Mutex
	Events []Event
}

func (r *Recorder) Publish(_ context.Context, e Event) error {
	r.mu.Lock()
	r.Events = append(r.Events, e)
	r.mu.Unlock()
	return nil
}

// Types returns the recorded event types in order.
func (r *Recorder) Types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.Events))
	for i, e := range r.Events {
		out[i] = e.Type
	}
	return out
}

// StockLowData is the payload of stock.low.
type StockLowData struct {
	ProductID string `json:"product_id"`
	Name      string `json:"name"`
	Unit      string `json:"unit,omitempty"`
	Stock     int64  `json:"stock"`     // thousandths
	MinStock  int64  `json:"min_stock"` // thousandths
}

// InvoiceOverdueData is the payload of invoice.overdue.
type InvoiceOverdueData struct {
	CustomerID   string       `json:"customer_id"`
	CustomerName string       `json:"customer_name"`
	InvoiceNo    string       `json:"invoice_no"`
	Amount       domain.Money `json:"amount"`
	DueDate      string       `json:"due_date"`
}

// PaymentReceivedData is the payload of payment.received.
type PaymentReceivedData struct {
	CustomerID string       `json:"customer_id"`
	Amount     domain.Money `json:"amount"`
	Reference  string       `json:"reference,omitempty"`
}
