package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/testdb"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

// pollDriver is a pull-based channel that records which listener runs each connection.
type pollDriver struct {
	kind domain.ChannelKind
	mu   sync.Mutex
	runs map[string]map[string]int // conn id -> owner -> concurrent pollers
	max  int
}

func (p *pollDriver) Kind() domain.ChannelKind { return p.kind }
func (p *pollDriver) Webhook(http.ResponseWriter, *http.Request, channels.Conn) ([]domain.InboundMessage, error) {
	return nil, nil
}
func (p *pollDriver) Send(context.Context, channels.Conn, domain.OutboundMessage) error { return nil }
func (p *pollDriver) Status(context.Context, channels.Conn) (string, error)             { return "ok", nil }

func (p *pollDriver) poll(owner string) func(ctx context.Context, c channels.Conn, _ func(context.Context, domain.InboundMessage) error) error {
	return func(ctx context.Context, c channels.Conn, _ func(context.Context, domain.InboundMessage) error) error {
		p.mu.Lock()
		if p.runs[c.ID] == nil {
			p.runs[c.ID] = map[string]int{}
		}
		p.runs[c.ID][owner]++
		total := 0
		for _, n := range p.runs[c.ID] {
			total += n
		}
		p.max = max(p.max, total)
		p.mu.Unlock()
		<-ctx.Done()
		p.mu.Lock()
		p.runs[c.ID][owner]--
		p.mu.Unlock()
		return nil
	}
}

// ownerDriver binds the shared recorder to one listener's identity.
type ownerDriver struct {
	*pollDriver
	owner string
}

func (o ownerDriver) Poll(ctx context.Context, c channels.Conn, sink func(context.Context, domain.InboundMessage) error) error {
	return o.poll(o.owner)(ctx, c, sink)
}

func (p *pollDriver) owners() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]string{}
	for id, by := range p.runs {
		for o, n := range by {
			if n > 0 {
				out[id] += o
			}
		}
	}
	return out
}

func TestListenerLeasesFailover(t *testing.T) {
	db := testdb.Open(t)
	ctx := context.Background()
	pg := &store.PG{DB: db}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	kind := domain.ChannelKind("itpoll" + randID(3))
	company := domain.CompanyID("itlease" + randID(3))
	drv := &pollDriver{kind: kind, runs: map[string]map[string]int{}}
	var ids []string
	for i := range 4 {
		c := store.Connection{ID: fmt.Sprintf("%s-%d", company, i), CompanyID: company, Kind: kind, Enabled: true, Config: []byte(`{}`)}
		if err := pg.UpsertConnection(ctx, &c); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM channel_connections WHERE company_id = $1`, string(company))
	})
	const ttl = 2 * time.Second
	start := func(owner string, pool *pgxpool.Pool, maxN int) context.CancelFunc {
		lctx, cancel := context.WithCancel(ctx)
		l := &worker.Listeners{DB: pool, Store: pg, Channels: channels.NewRegistry(ownerDriver{drv, owner}), Queue: nil, Log: log, Owner: owner, TTL: ttl, Max: maxN}
		done := make(chan struct{})
		go func() { l.Run(lctx); close(done) }()
		return func() { cancel(); <-done }
	}
	wantOwners := func(what string, ok func(map[string]string) bool) {
		t.Helper()
		waitFor(t, 4*ttl, what, func() (bool, string) {
			o := drv.owners()
			return ok(o), fmt.Sprint(o)
		})
	}
	poolA, err := pgxpool.New(ctx, db.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	stopA := start("A", poolA, 3)
	wantOwners("A holds its max", func(o map[string]string) bool {
		n := 0
		for _, id := range ids {
			if o[id] == "A" {
				n++
			}
		}
		return n == 3
	})
	stopB := start("B", db, 10)
	defer stopB()
	wantOwners("B picks up the remainder", func(o map[string]string) bool {
		a, b := 0, 0
		for _, id := range ids {
			switch o[id] {
			case "A":
				a++
			case "B":
				b++
			}
		}
		return a == 3 && b == 1
	})
	time.Sleep(ttl)
	if o := drv.owners(); len(o) != 4 {
		t.Fatalf("lease churn while both alive: %v", o)
	}
	crashAt := time.Now()
	poolA.Close() // A loses the database: it cannot renew or release its leases
	stopA()
	wantOwners("B takes over A's connections after the TTL", func(o map[string]string) bool {
		for _, id := range ids {
			if o[id] != "B" {
				return false
			}
		}
		return true
	})
	if took := time.Since(crashAt); took > 2*ttl {
		t.Fatalf("failover took %s (ttl %s)", took, ttl)
	}
	drv.mu.Lock()
	peak := drv.max
	drv.mu.Unlock()
	if peak != 1 {
		t.Fatalf("a connection was polled by %d listeners at once", peak)
	}
}

// countingProvider measures concurrent Handle calls per company.
type countingProvider struct {
	dataprovider.Provider
	delay time.Duration
	mu    sync.Mutex
	cur   map[domain.CompanyID]int
	peak  map[domain.CompanyID]int
	done  map[domain.CompanyID][]time.Time
}

func (p *countingProvider) FindProducts(ctx context.Context, c domain.CompanyID, q string, limit int) ([]dataprovider.Product, error) {
	p.mu.Lock()
	p.cur[c]++
	p.peak[c] = max(p.peak[c], p.cur[c])
	p.mu.Unlock()
	time.Sleep(p.delay)
	out, err := p.Provider.FindProducts(ctx, c, q, limit)
	p.mu.Lock()
	p.cur[c]--
	p.done[c] = append(p.done[c], time.Now())
	p.mu.Unlock()
	return out, err
}

func TestPerCompanyFairness(t *testing.T) {
	cp := &countingProvider{delay: 80 * time.Millisecond, cur: map[domain.CompanyID]int{}, peak: map[domain.CompanyID]int{}, done: map[domain.CompanyID][]time.Time{}}
	s := newStack(t, stackOpts{inflight: 2, concurrency: 6, wrapData: func(p dataprovider.Provider) dataprovider.Provider {
		cp.Provider = p
		return cp
	}})
	noisy := s.company("noisy")
	for range 24 {
		s.async(noisy, "owner", "stok toner")
	}
	quiet := make([]domain.CompanyID, 4)
	for i := range quiet {
		quiet[i] = s.company(fmt.Sprintf("quiet%d", i))
		s.async(quiet[i], "owner", "stok toner")
	}
	for _, c := range quiet {
		s.waitOutbox(c, "owner", "Toner 85A stok")
	}
	waitFor(t, 30*time.Second, "noisy backlog", func() (bool, string) {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		return len(cp.done[noisy]) == 24, fmt.Sprint(len(cp.done[noisy]))
	})
	cp.mu.Lock()
	defer cp.mu.Unlock()
	if cp.peak[noisy] > 2 {
		t.Fatalf("noisy company ran %d jobs at once with PER_COMPANY_INFLIGHT=2", cp.peak[noisy])
	}
	lastNoisy := cp.done[noisy][len(cp.done[noisy])-1]
	for _, c := range quiet {
		if !cp.done[c][0].Before(lastNoisy) {
			t.Fatalf("%s waited behind the noisy backlog", c)
		}
	}
}

func TestRAGIsolation(t *testing.T) {
	s := newStack(t, stackOpts{})
	ctx := context.Background()
	a, b := s.company("raga"), s.company("ragb")
	s.async(a, "owner", "not al: depo kirası her ayın 5'inde Demir Yapı'ya ödenir")
	s.waitOutbox(a, "owner", "Not aldım")
	s.async(b, "owner", "not al: depo kirası nakit olarak Kaya Gıda'ya verilir")
	s.waitOutbox(b, "owner", "Not aldım")
	s.async(a, "owner", "Ahmet Yılmaz'dan 3.000 TL tahsilat")
	s.waitOutbox(a, "owner", "Kaydedildi")
	waitFor(t, 15*time.Second, "indexing", func() (bool, string) {
		ha, _ := s.index.Search(ctx, a, "depo kirası", 5)
		hb, _ := s.index.Search(ctx, b, "depo kirası", 5)
		return len(ha) >= 2 && len(hb) >= 1, fmt.Sprint(len(ha), len(hb))
	})
	for _, c := range []domain.CompanyID{a, b} {
		hits, err := s.index.Search(ctx, c, "depo kirası Demir Yapı Kaya Gıda tahsilat", 20)
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range hits {
			if c == b && (strings.Contains(h.Content, "Demir") || strings.Contains(h.Content, "Tahsilat")) {
				t.Fatalf("company b sees a's knowledge: %+v", h)
			}
			if c == a && strings.Contains(h.Content, "Kaya Gıda") {
				t.Fatalf("company a sees b's knowledge: %+v", h)
			}
		}
	}
	s.async(a, "owner", "notlarda depo kirası ara")
	got := s.waitOutbox(a, "owner", "[not]")
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "Demir Yapı") || strings.Contains(joined, "Kaya Gıda") {
		t.Fatalf("a's search: %s", joined)
	}
	s.async(b, "owner", "kayıtlarda tahsilat ara")
	s.async(b, "owner", "yardım")
	got = s.waitOutbox(b, "owner", "Yapabileceklerim")
	if strings.Contains(strings.Join(got, "\n"), "3.000,00 TL") {
		t.Fatalf("b found a's records: %q", got)
	}
}
