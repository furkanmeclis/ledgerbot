package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Batched wraps PG and writes audit rows in batches with COPY. The audit log
// is the hottest write path (several rows per message); batching turns many
// small commits into one COPY every Interval or Size rows.
//
// Trade-off: up to Interval of audit rows can be lost on a hard crash. When the
// buffer is full, Audit falls back to a synchronous insert (backpressure, no loss).
type Batched struct {
	*PG
	ch   chan AuditEntry
	done chan struct{}
	log  *slog.Logger
}

// NewBatched starts the flusher. Call Close on shutdown.
func NewBatched(p *PG, size int, interval time.Duration, log *slog.Logger) *Batched {
	b := &Batched{PG: p, ch: make(chan AuditEntry, size*4), done: make(chan struct{}), log: log}
	go b.loop(size, interval)
	return b
}

func (b *Batched) Audit(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	select {
	case b.ch <- e:
		return nil
	default:
		return b.PG.Audit(ctx, e)
	}
}

// Close flushes pending rows.
func (b *Batched) Close() {
	close(b.ch)
	<-b.done
}

func (b *Batched) loop(size int, interval time.Duration) {
	defer close(b.done)
	buf := make([]AuditEntry, 0, size)
	t := time.NewTicker(interval)
	defer t.Stop()
	flush := func() {
		if len(buf) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rows := make([][]any, len(buf))
		for i, e := range buf {
			rows[i] = []any{string(e.CompanyID), string(e.ActorID), string(e.Channel), e.Action, e.DraftID, nullJSON(e.Detail), e.At}
		}
		if _, err := b.DB.CopyFrom(ctx, pgx.Identifier{"audit_log"},
			[]string{"company_id", "actor_id", "channel", "action", "draft_id", "detail", "at"}, pgx.CopyFromRows(rows)); err != nil {
			b.log.Error("audit batch failed", "rows", len(buf), "err", err)
		}
		buf = buf[:0]
	}
	for {
		select {
		case e, open := <-b.ch:
			if !open {
				flush()
				return
			}
			buf = append(buf, e)
			if len(buf) >= size {
				flush()
			}
		case <-t.C:
			flush()
		}
	}
}
