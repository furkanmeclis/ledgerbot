// Package store persists ledgerbot's own state: drafts, duplicate
// fingerprints, AI usage, audit log, deletion requests, channel connections,
// API idempotency records and channel leases. Accounting data itself lives
// behind dataprovider and is never stored here.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned on a state transition that is no longer valid
// (e.g. approving a draft that was already committed).
var ErrConflict = errors.New("store: conflict")

// DraftFilter narrows a draft listing.
type DraftFilter struct {
	Status  domain.DraftStatus
	ActorID domain.UserID // set for "own" scope roles
	Before  time.Time     // keyset cursor on created_at
	Limit   int
}

// AuditEntry is one append-only audit row.
type AuditEntry struct {
	ID        int64              `json:"id"`
	CompanyID domain.CompanyID   `json:"company_id"`
	ActorID   domain.UserID      `json:"actor_id,omitempty"`
	Channel   domain.ChannelKind `json:"channel,omitempty"`
	Action    string             `json:"action"` // message.received, intent.parsed, draft.created, draft.committed, permission.denied ...
	DraftID   string             `json:"draft_id,omitempty"`
	Detail    json.RawMessage    `json:"detail,omitempty"`
	At        time.Time          `json:"at"`
}

// UsageRecord is one AI call for cost tracking and quotas.
type UsageRecord struct {
	CompanyID    domain.CompanyID
	Purpose      string
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	AudioSeconds float64
	CostMicroUSD int64
	At           time.Time
}

// UsageSummary aggregates usage for reporting.
type UsageSummary struct {
	Purpose      string  `json:"purpose"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Calls        int64   `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	AudioSeconds float64 `json:"audio_seconds"`
	CostUSD      float64 `json:"cost_usd"`
}

// DeletionRequest is a data-subject deletion request (KVKK / GDPR / Meta).
type DeletionRequest struct {
	ID          string             `json:"id"`
	CompanyID   domain.CompanyID   `json:"company_id,omitempty"` // empty = all companies (Meta callback)
	Channel     domain.ChannelKind `json:"channel,omitempty"`
	Subject     string             `json:"subject"` // channel address: phone, chat id, email, or meta user id
	Status      string             `json:"status"`  // pending, completed, failed
	Code        string             `json:"confirmation_code"`
	RequestedAt time.Time          `json:"requested_at"`
	CompletedAt *time.Time         `json:"completed_at,omitempty"`
	Removed     int64              `json:"removed_rows"`
}

// Connection is a channel connection owned by ledgerbot (bot tokens, WuzAPI
// user tokens, WABA phone ids, IMAP credentials). Config is encrypted at rest
// by the caller before it reaches the store.
type Connection struct {
	ID        string             `json:"id"`
	CompanyID domain.CompanyID   `json:"company_id"` // empty for a central (shared) connection
	Kind      domain.ChannelKind `json:"kind"`
	Mode      string             `json:"mode,omitempty"` // wuzapi: "per_company" | "central"
	Name      string             `json:"name"`
	Enabled   bool               `json:"enabled"`
	Config    []byte             `json:"-"`
	Status    string             `json:"status"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// IdemRecord is a stored REST response for an Idempotency-Key.
type IdemRecord struct {
	Status int
	Body   []byte
	Hash   string // request body hash; a different body with the same key is rejected
}

// Store is implemented by PG (production) and Mem (tests).
type Store interface {
	// Drafts
	CreateDraft(ctx context.Context, d *domain.Draft) error
	GetDraft(ctx context.Context, c domain.CompanyID, id string) (domain.Draft, error)
	// TransitionDraft moves a draft from one status to another atomically.
	TransitionDraft(ctx context.Context, c domain.CompanyID, id string, from, to domain.DraftStatus, externalID string) (domain.Draft, error)
	ListDrafts(ctx context.Context, c domain.CompanyID, f DraftFilter) ([]domain.Draft, error)
	// LastCommitted returns the actor's most recent committed draft of the given capabilities (memory).
	LastCommitted(ctx context.Context, c domain.CompanyID, actor domain.UserID, capabilities []string) (domain.Draft, error)
	// LatestPending returns the newest pending draft for a sender (approval flow).
	LatestPending(ctx context.Context, c domain.CompanyID, ch domain.ChannelKind, sender string, maxAge time.Duration) (domain.Draft, error)

	// Duplicate detection: returns the earlier draft id if the fingerprint was seen.
	ClaimFingerprint(ctx context.Context, c domain.CompanyID, fp, draftID string) (existingDraftID string, dup bool, err error)

	// Usage and quotas
	RecordUsage(ctx context.Context, u UsageRecord) error
	CallsSince(ctx context.Context, c domain.CompanyID, since time.Time) (int64, error)
	UsageReport(ctx context.Context, c domain.CompanyID, from, to time.Time) ([]UsageSummary, error)

	// Audit
	Audit(ctx context.Context, e AuditEntry) error
	ListAudit(ctx context.Context, c domain.CompanyID, beforeID int64, limit int) ([]AuditEntry, error)

	// Deletion
	CreateDeletion(ctx context.Context, r DeletionRequest) error
	GetDeletion(ctx context.Context, id string) (DeletionRequest, error)
	// EraseSubject deletes drafts and fingerprints of a subject and redacts audit rows.
	EraseSubject(ctx context.Context, r DeletionRequest) (int64, error)
	CompleteDeletion(ctx context.Context, id string, removed int64) error

	// Channel connections
	UpsertConnection(ctx context.Context, c *Connection) error
	GetConnection(ctx context.Context, id string) (Connection, error)
	ListConnections(ctx context.Context, c domain.CompanyID) ([]Connection, error)
	DeleteConnection(ctx context.Context, c domain.CompanyID, id string) error
	SetConnectionStatus(ctx context.Context, id, status string) error

	// Idempotency for the REST API
	GetIdem(ctx context.Context, c domain.CompanyID, key string) (IdemRecord, bool, error)
	PutIdem(ctx context.Context, c domain.CompanyID, key string, r IdemRecord, ttl time.Duration) error

	// Blobs hold media for queued jobs.
	PutBlob(ctx context.Context, id string, data []byte, ttl time.Duration) error
	GetBlob(ctx context.Context, id string) ([]byte, error)
	DeleteBlob(ctx context.Context, id string) error
	// Cleanup removes expired blobs, idempotency keys and throttles.
	Cleanup(ctx context.Context) (int64, error)

	// ThrottleOnce returns true the first time key is seen within ttl (alert throttling).
	ThrottleOnce(ctx context.Context, c domain.CompanyID, key string, ttl time.Duration) (bool, error)
}
