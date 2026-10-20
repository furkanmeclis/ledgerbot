// Package channels adapts messaging platforms to ledgerbot's normalized
// messages. Every driver implements Driver; pull-based platforms (Telegram
// long polling, IMAP) also implement Poller and are run by workers that hold
// a lease on the connection.
//
// Connection modes for WhatsApp:
//   - wuzapi per_company: each company scans its own QR on its own WuzAPI user.
//   - wuzapi central:     one shared number; senders are routed to companies
//     through the host directory (tenant.Directory).
//   - waba:               Meta WhatsApp Cloud API (per company or central).
package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

// MaxBody bounds webhook bodies (WuzAPI inlines media as base64).
const MaxBody = 32 << 20

// ErrSignature is returned when a webhook fails verification.
var ErrSignature = errors.New("channels: invalid webhook signature")

// ErrHandled means the driver already wrote the HTTP response (e.g. Meta's
// verification challenge) and there is nothing to enqueue.
var ErrHandled = errors.New("channels: handled")

// Conn is a connection with its decrypted config.
type Conn struct {
	store.Connection
	Config json.RawMessage
}

// Driver is a channel family.
type Driver interface {
	Kind() domain.ChannelKind
	// Webhook verifies and parses an inbound HTTP push. CompanyID is set from the
	// connection; central connections leave it empty for directory routing.
	Webhook(w http.ResponseWriter, r *http.Request, c Conn) ([]domain.InboundMessage, error)
	// Send delivers a reply.
	Send(ctx context.Context, c Conn, out domain.OutboundMessage) error
	// Status reports connection health ("connected", "logged_out", "error: ...").
	Status(ctx context.Context, c Conn) (string, error)
}

// Poller is implemented by pull-based drivers. Poll blocks until ctx is done.
type Poller interface {
	Poll(ctx context.Context, c Conn, sink func(context.Context, domain.InboundMessage) error) error
}

// Registry maps kinds to drivers.
type Registry struct{ drivers map[domain.ChannelKind]Driver }

// NewRegistry builds a registry.
func NewRegistry(ds ...Driver) *Registry {
	r := &Registry{drivers: make(map[domain.ChannelKind]Driver, len(ds))}
	for _, d := range ds {
		r.drivers[d.Kind()] = d
	}
	return r
}

// Get returns a driver.
func (r *Registry) Get(k domain.ChannelKind) (Driver, bool) {
	d, ok := r.drivers[k]
	return d, ok
}

// Kinds lists registered kinds.
func (r *Registry) Kinds() []domain.ChannelKind {
	out := make([]domain.ChannelKind, 0, len(r.drivers))
	for k := range r.drivers {
		out = append(out, k)
	}
	return out
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func readBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, MaxBody))
}

// doJSON sends a JSON request and decodes a JSON response into out (if non-nil).
func doJSON(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("channels: %s %s: status %d: %.300s", method, url, resp.StatusCode, data)
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

// NormalizeMIME strips parameters and resolves aliases ("audio/ogg; codecs=opus" -> "audio/ogg").
func NormalizeMIME(m string) string {
	m = strings.ToLower(strings.TrimSpace(strings.SplitN(m, ";", 2)[0]))
	if m == "application/ogg" {
		return "audio/ogg"
	}
	return m
}

// KindFromMIME maps a MIME type to a message kind.
func KindFromMIME(m string) domain.MessageKind {
	switch {
	case strings.HasPrefix(m, "image/"):
		return domain.KindImage
	case strings.HasPrefix(m, "audio/"):
		return domain.KindAudio
	default:
		return domain.KindDocument
	}
}
