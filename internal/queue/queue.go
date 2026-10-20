// Package queue defines ledgerbot's durable jobs on top of River (PostgreSQL).
//
// Why River: no extra infrastructure, transactional inserts, FOR UPDATE SKIP
// LOCKED job claiming (no leader needed for work distribution), retries with
// backoff, and a dead-letter state ("discarded") we expose through the API.
// The Enqueuer interface keeps the rest of the code independent of River.
package queue

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
)

// Queue names. Separate queues isolate slow AI work from fast deliveries.
const (
	QueueInbound  = "inbound"  // message understanding (AI calls)
	QueueOutbound = "outbound" // channel sends
	QueueEvents   = "events"   // rules, alerts, webhook fan-out
	QueueWebhooks = "webhooks" // outbound HTTP deliveries
	QueueMaint    = "maintenance"
)

// InboundArgs carries one inbound message. Media bytes are stored as a blob
// so job rows stay small.
type InboundArgs struct {
	Msg    domain.InboundMessage `json:"msg"`
	BlobID string                `json:"blob_id,omitempty"`
}

func (InboundArgs) Kind() string { return "inbound_message" }
func (a InboundArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{
		Queue: QueueInbound, MaxAttempts: 5,
		// Channels may redeliver webhooks; one job per message id.
		UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 24 * time.Hour},
	}
}

// OutboundArgs carries one reply.
type OutboundArgs struct {
	Out domain.OutboundMessage `json:"out"`
}

func (OutboundArgs) Kind() string { return "outbound_message" }
func (OutboundArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueOutbound, MaxAttempts: 8}
}

// EventArgs carries an event to process (rules + webhook fan-out).
type EventArgs struct {
	Event events.Event `json:"event"`
}

func (EventArgs) Kind() string { return "event" }
func (EventArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueEvents, MaxAttempts: 5}
}

// WebhookArgs is one delivery to one subscriber.
type WebhookArgs struct {
	URL    string       `json:"url"`
	Secret string       `json:"secret"`
	Event  events.Event `json:"event"`
}

func (WebhookArgs) Kind() string { return "webhook_delivery" }
func (WebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueWebhooks, MaxAttempts: 12}
}

// DeletionArgs runs a data deletion request.
type DeletionArgs struct {
	ID string `json:"id"`
}

func (DeletionArgs) Kind() string { return "data_deletion" }
func (DeletionArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaint, MaxAttempts: 10}
}

// SimulateArgs makes the fake world emit events (demo / load tests).
type SimulateArgs struct {
	Companies []domain.CompanyID `json:"companies,omitempty"`
}

func (SimulateArgs) Kind() string { return "simulate" }
func (SimulateArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaint, MaxAttempts: 1}
}

// CleanupArgs removes expired rows.
type CleanupArgs struct{}

func (CleanupArgs) Kind() string { return "cleanup" }
func (CleanupArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueMaint, MaxAttempts: 1}
}

// Enqueuer inserts jobs.
type Enqueuer interface {
	Enqueue(ctx context.Context, args river.JobArgs) (int64, error)
	EnqueueMany(ctx context.Context, args []river.JobArgs) error
}

// River adapts a River client.
type River struct{ C *river.Client[pgx.Tx] }

func (r River) Enqueue(ctx context.Context, args river.JobArgs) (int64, error) {
	res, err := r.C.Insert(ctx, args, nil)
	if err != nil {
		return 0, err
	}
	return res.Job.ID, nil
}

func (r River) EnqueueMany(ctx context.Context, args []river.JobArgs) error {
	if len(args) == 0 {
		return nil
	}
	params := make([]river.InsertManyParams, len(args))
	for i, a := range args {
		params[i] = river.InsertManyParams{Args: a}
	}
	_, err := r.C.InsertMany(ctx, params)
	return err
}

// Publisher publishes events as jobs.
type Publisher struct{ Q Enqueuer }

func (p Publisher) Publish(ctx context.Context, e events.Event) error {
	_, err := p.Q.Enqueue(ctx, EventArgs{Event: e})
	return err
}

// Queues returns the River queue config for a worker process.
func Queues(concurrency int) map[string]river.QueueConfig {
	c := max(concurrency, 1)
	return map[string]river.QueueConfig{
		QueueInbound:  {MaxWorkers: c},
		QueueOutbound: {MaxWorkers: c},
		QueueEvents:   {MaxWorkers: max(c/2, 1)},
		QueueWebhooks: {MaxWorkers: c},
		QueueMaint:    {MaxWorkers: 2},
	}
}
