package tenant

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Cache wraps a Source with a TTL cache, request coalescing and a safe fallback.
//
// Hot path is a single RLock + map lookup. When an entry expires, one caller
// refreshes it (singleflight) while others keep using the stale value. If the
// source fails, the stale snapshot is served as Degraded (writes off). If there
// is no snapshot at all, the error is returned and the caller must refuse.
type Cache struct {
	src Source
	ttl time.Duration
	log *slog.Logger

	mu      sync.RWMutex
	entries map[domain.CompanyID]*Snapshot
	group   singleflight.Group
}

// NewCache returns a cache with the given TTL.
func NewCache(src Source, ttl time.Duration, log *slog.Logger) *Cache {
	return &Cache{src: src, ttl: ttl, log: log, entries: make(map[domain.CompanyID]*Snapshot)}
}

// Get returns the snapshot for a company.
func (c *Cache) Get(ctx context.Context, company domain.CompanyID) (*Snapshot, error) {
	c.mu.RLock()
	snap, ok := c.entries[company]
	c.mu.RUnlock()
	if ok && time.Since(snap.FetchedAt) < c.ttl {
		return snap, nil
	}
	if ok {
		// Stale: refresh in the background, serve stale now.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = c.refresh(ctx, company)
		}()
		return snap, nil
	}
	return c.refresh(ctx, company)
}

// Invalidate drops a cached snapshot (called by the settings-changed webhook).
func (c *Cache) Invalidate(company domain.CompanyID) {
	c.mu.Lock()
	delete(c.entries, company)
	c.mu.Unlock()
}

func (c *Cache) refresh(ctx context.Context, company domain.CompanyID) (*Snapshot, error) {
	v, err, _ := c.group.Do(string(company), func() (any, error) {
		s, err := c.src.Settings(ctx, company)
		if err != nil {
			return nil, err
		}
		m, err := c.src.Matrix(ctx, company)
		if err != nil {
			return nil, err
		}
		s.CompanyID, m.CompanyID = company, company
		snap := NewSnapshot(s, m)
		c.mu.Lock()
		c.entries[company] = snap
		c.mu.Unlock()
		return snap, nil
	})
	if err == nil {
		return v.(*Snapshot), nil
	}
	if errors.Is(err, ErrUnknownCompany) {
		return nil, err
	}
	c.log.Warn("tenant source unavailable", "company", company, "err", err)
	c.mu.RLock()
	stale, ok := c.entries[company]
	c.mu.RUnlock()
	if ok {
		d := *stale
		d.Degraded = true
		return &d, nil
	}
	return nil, err
}
