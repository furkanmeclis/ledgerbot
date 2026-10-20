package channels

import (
	"context"
	"net/http"
	"sync"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// DevOutbox persists dev replies so every replica (and the demo dashboard)
// sees them. store.PG implements it on PostgreSQL with NOTIFY.
type DevOutbox interface {
	AppendDev(ctx context.Context, out domain.OutboundMessage) error
	ListDev(ctx context.Context, c domain.CompanyID, to string, limit int) ([]domain.OutboundMessage, error)
}

// Dev is an in-process channel for development, demos and load tests.
// Inbound messages come from POST /v1/dev/messages; replies go to the outbox.
type Dev struct {
	Box DevOutbox // nil = in-memory, single process

	mu    sync.Mutex
	mem   map[string][]domain.OutboundMessage
	limit int
}

// NewDev returns a dev channel; limit bounds the in-memory outbox per sender.
func NewDev(limit int, box DevOutbox) *Dev {
	return &Dev{Box: box, mem: map[string][]domain.OutboundMessage{}, limit: limit}
}

func (*Dev) Kind() domain.ChannelKind { return domain.ChannelDev }

func (*Dev) Webhook(http.ResponseWriter, *http.Request, Conn) ([]domain.InboundMessage, error) {
	return nil, nil
}

func (d *Dev) Send(ctx context.Context, _ Conn, out domain.OutboundMessage) error {
	if d.Box != nil {
		return d.Box.AppendDev(ctx, out)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	k := string(out.CompanyID) + "|" + out.To
	box := append(d.mem[k], out)
	if len(box) > d.limit {
		box = box[len(box)-d.limit:]
	}
	d.mem[k] = box
	return nil
}

func (*Dev) Status(context.Context, Conn) (string, error) { return "connected", nil }

// Outbox returns the latest replies sent to a dev sender, oldest first.
func (d *Dev) Outbox(ctx context.Context, c domain.CompanyID, to string) ([]domain.OutboundMessage, error) {
	if d.Box != nil {
		return d.Box.ListDev(ctx, c, to, d.limit)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]domain.OutboundMessage(nil), d.mem[string(c)+"|"+to]...), nil
}
