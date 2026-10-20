package store

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Mem is an in-memory Store for unit tests and the single-binary demo.
type Mem struct {
	mu        sync.Mutex
	drafts    map[string]domain.Draft
	fps       map[string]string
	usage     []UsageRecord
	audit     []AuditEntry
	deletions map[string]DeletionRequest
	conns     map[string]Connection
	idem      map[string]IdemRecord
	throttle  map[string]time.Time
	blobs     map[string][]byte
}

// NewMem returns an empty Mem store.
func NewMem() *Mem {
	return &Mem{
		drafts: map[string]domain.Draft{}, fps: map[string]string{},
		deletions: map[string]DeletionRequest{}, conns: map[string]Connection{}, idem: map[string]IdemRecord{}, throttle: map[string]time.Time{}, blobs: map[string][]byte{},
	}
}

var _ Store = (*Mem)(nil)

func (m *Mem) CreateDraft(_ context.Context, d *domain.Draft) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	d.UpdatedAt = now
	m.drafts[d.ID] = *d
	return nil
}

func (m *Mem) GetDraft(_ context.Context, c domain.CompanyID, id string) (domain.Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	if !ok || d.CompanyID != c {
		return domain.Draft{}, ErrNotFound
	}
	return d, nil
}

func (m *Mem) TransitionDraft(_ context.Context, c domain.CompanyID, id string, from, to domain.DraftStatus, ext string) (domain.Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[id]
	if !ok || d.CompanyID != c {
		return domain.Draft{}, ErrNotFound
	}
	if d.Status != from {
		return d, ErrConflict
	}
	d.Status, d.UpdatedAt = to, time.Now().UTC()
	if ext != "" {
		d.ExternalID = ext
	}
	m.drafts[id] = d
	return d, nil
}

func (m *Mem) ListDrafts(_ context.Context, c domain.CompanyID, f DraftFilter) ([]domain.Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.Draft
	for _, d := range m.drafts {
		if d.CompanyID != c || (f.Status != "" && d.Status != f.Status) || (f.ActorID != "" && d.ActorID != f.ActorID) ||
			(!f.Before.IsZero() && !d.CreatedAt.Before(f.Before)) {
			continue
		}
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b domain.Draft) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *Mem) LatestPending(_ context.Context, c domain.CompanyID, ch domain.ChannelKind, sender string, maxAge time.Duration) (domain.Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best domain.Draft
	cutoff := time.Now().Add(-maxAge)
	for _, d := range m.drafts {
		if d.CompanyID == c && d.Channel == ch && d.Sender == sender && d.Status == domain.DraftPending &&
			d.CreatedAt.After(cutoff) && d.CreatedAt.After(best.CreatedAt) {
			best = d
		}
	}
	if best.ID == "" {
		return best, ErrNotFound
	}
	return best, nil
}

func (m *Mem) ClaimFingerprint(_ context.Context, c domain.CompanyID, fp, draftID string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(c) + "|" + fp
	if prev, ok := m.fps[k]; ok {
		return prev, true, nil
	}
	m.fps[k] = draftID
	return "", false, nil
}

func (m *Mem) RecordUsage(_ context.Context, u UsageRecord) error {
	m.mu.Lock()
	m.usage = append(m.usage, u)
	m.mu.Unlock()
	return nil
}

func (m *Mem) CallsSince(_ context.Context, c domain.CompanyID, since time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, u := range m.usage {
		if u.CompanyID == c && !u.At.Before(since) {
			n++
		}
	}
	return n, nil
}

func (m *Mem) UsageReport(_ context.Context, c domain.CompanyID, from, to time.Time) ([]UsageSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	idx := map[string]*UsageSummary{}
	var keys []string
	for _, u := range m.usage {
		if u.CompanyID != c || u.At.Before(from) || !u.At.Before(to) {
			continue
		}
		k := u.Purpose + "|" + u.Provider + "|" + u.Model
		s, ok := idx[k]
		if !ok {
			s = &UsageSummary{Purpose: u.Purpose, Provider: u.Provider, Model: u.Model}
			idx[k] = s
			keys = append(keys, k)
		}
		s.Calls++
		s.InputTokens += int64(u.InputTokens)
		s.OutputTokens += int64(u.OutputTokens)
		s.AudioSeconds += u.AudioSeconds
		s.CostUSD += float64(u.CostMicroUSD) / 1e6
	}
	out := make([]UsageSummary, 0, len(keys))
	for _, k := range keys {
		out = append(out, *idx[k])
	}
	return out, nil
}

func (m *Mem) Audit(_ context.Context, e AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.ID = int64(len(m.audit) + 1)
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	m.audit = append(m.audit, e)
	return nil
}

func (m *Mem) ListAudit(_ context.Context, c domain.CompanyID, beforeID int64, limit int) ([]AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEntry
	for i := len(m.audit) - 1; i >= 0; i-- {
		e := m.audit[i]
		if e.CompanyID == c && (beforeID == 0 || e.ID < beforeID) {
			out = append(out, e)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *Mem) CreateDeletion(_ context.Context, r DeletionRequest) error {
	m.mu.Lock()
	m.deletions[r.ID] = r
	m.mu.Unlock()
	return nil
}

func (m *Mem) GetDeletion(_ context.Context, id string) (DeletionRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.deletions[id]
	if !ok {
		return r, ErrNotFound
	}
	return r, nil
}

func (m *Mem) EraseSubject(_ context.Context, r DeletionRequest) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, d := range m.drafts {
		if d.Sender == r.Subject && (r.CompanyID == "" || d.CompanyID == r.CompanyID) {
			delete(m.drafts, id)
			n++
		}
	}
	for i, e := range m.audit {
		if (r.CompanyID == "" || e.CompanyID == r.CompanyID) && strings.Contains(string(e.Detail), r.Subject) {
			m.audit[i].Detail = json.RawMessage(`{"redacted":true}`)
			n++
		}
	}
	return n, nil
}

func (m *Mem) CompleteDeletion(_ context.Context, id string, removed int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.deletions[id]
	if !ok {
		return ErrNotFound
	}
	now := time.Now().UTC()
	r.Status, r.CompletedAt, r.Removed = "completed", &now, removed
	m.deletions[id] = r
	return nil
}

func (m *Mem) UpsertConnection(_ context.Context, c *Connection) error {
	m.mu.Lock()
	c.UpdatedAt = time.Now().UTC()
	m.conns[c.ID] = *c
	m.mu.Unlock()
	return nil
}

func (m *Mem) GetConnection(_ context.Context, id string) (Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok {
		return c, ErrNotFound
	}
	return c, nil
}

func (m *Mem) ListConnections(_ context.Context, company domain.CompanyID) ([]Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Connection
	for _, c := range m.conns {
		if company == "" || c.CompanyID == company {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Connection) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func (m *Mem) DeleteConnection(_ context.Context, company domain.CompanyID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok || c.CompanyID != company {
		return ErrNotFound
	}
	delete(m.conns, id)
	return nil
}

func (m *Mem) SetConnectionStatus(_ context.Context, id, status string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conns[id]
	if !ok {
		return ErrNotFound
	}
	c.Status = status
	m.conns[id] = c
	return nil
}

func (m *Mem) GetIdem(_ context.Context, c domain.CompanyID, key string) (IdemRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.idem[string(c)+"|"+key]
	return r, ok, nil
}

func (m *Mem) PutIdem(_ context.Context, c domain.CompanyID, key string, r IdemRecord, _ time.Duration) error {
	m.mu.Lock()
	m.idem[string(c)+"|"+key] = r
	m.mu.Unlock()
	return nil
}

func (m *Mem) ThrottleOnce(_ context.Context, c domain.CompanyID, key string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := string(c) + "|" + key
	if until, ok := m.throttle[k]; ok && time.Now().Before(until) {
		return false, nil
	}
	m.throttle[k] = time.Now().Add(ttl)
	return true, nil
}

func (m *Mem) PutBlob(_ context.Context, id string, data []byte, _ time.Duration) error {
	m.mu.Lock()
	m.blobs[id] = data
	m.mu.Unlock()
	return nil
}

func (m *Mem) GetBlob(_ context.Context, id string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[id]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

func (m *Mem) DeleteBlob(_ context.Context, id string) error {
	m.mu.Lock()
	delete(m.blobs, id)
	m.mu.Unlock()
	return nil
}

func (m *Mem) Cleanup(context.Context) (int64, error) { return 0, nil }
