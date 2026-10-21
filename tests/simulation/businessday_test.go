package simulation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

// dispatcher mirrors the worker's rule engine (rules → users → throttle →
// first dev address) so alerts can be delivered without River/PostgreSQL.
type dispatcher struct {
	e *simEnv
}

func qtyText(q int64) string {
	if q%1000 == 0 {
		return strconv.FormatInt(q/1000, 10)
	}
	return strconv.FormatFloat(float64(q)/1000, 'f', -1, 64)
}

func (d dispatcher) handle(ctx context.Context, ev events.Event) (int, error) {
	snap, err := d.e.tenants.Get(ctx, ev.CompanyID)
	if err != nil {
		return 0, err
	}
	var text, entity string
	switch ev.Type {
	case events.StockLow:
		var p events.StockLowData
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return 0, err
		}
		text, entity = assistant.T("tr", "alert_stock", p.Name, qtyText(p.Stock), p.Unit, qtyText(p.MinStock)), p.ProductID
	case events.InvoiceOverdue:
		var p events.InvoiceOverdueData
		if err := json.Unmarshal(ev.Data, &p); err != nil {
			return 0, err
		}
		text, entity = assistant.T("tr", "alert_overdue", p.CustomerName, p.Amount.String(), p.InvoiceNo, p.DueDate), p.InvoiceNo
	default:
		return 0, nil
	}
	sent := 0
	for _, rule := range snap.Settings.Rules {
		if !tenant.Match(rule.Event, ev.Type) {
			continue
		}
		ttl, _ := time.ParseDuration(rule.Throttle)
		for _, u := range snap.UsersWithRoles(rule.NotifyRoles) {
			if ok, err := d.e.st.ThrottleOnce(ctx, ev.CompanyID, ev.Type+"|"+entity+"|"+string(u.ID), ttl); err != nil || !ok {
				continue
			}
			addr := u.Addresses[domain.ChannelDev]
			if err := d.e.dev.Send(ctx, channels.Conn{}, domain.OutboundMessage{CompanyID: ev.CompanyID, Channel: domain.ChannelDev, To: addr, Text: text}); err != nil {
				return sent, err
			}
			sent++
		}
	}
	return sent, nil
}

// TestBusinessDay runs the fake world's event simulator hour by hour for many
// companies while users keep working, and checks that low-stock and overdue
// alerts reach the right people exactly once and match the data.
func TestBusinessDay(t *testing.T) {
	companies := scaled(60)
	hours := 12
	rounds := 4 // simulator rounds per hour
	e := newSimEnv(t, simOpts{prefixes: map[string]domain.SaveMode{"bd": domain.SaveAuto}, devLimit: 100_000})
	ctx := context.Background()
	disp := dispatcher{e: e}
	st := &stats{}
	var mu sync.Mutex
	evCount := map[string]int{}
	lowSeen := map[domain.CompanyID]map[string]bool{}
	alerts := 0
	start := time.Now()
	for h := range hours {
		pool(companies, 32, func(i int) {
			c := domain.CompanyID(fmt.Sprintf("bd%03d", i))
			for range rounds {
				t0 := time.Now()
				evs, err := worker.Simulate(ctx, e.data, c)
				if err != nil {
					st.fail(time.Since(t0), "%s simulate: %v", c, err)
					continue
				}
				st.ok(time.Since(t0))
				for _, ev := range evs {
					t1 := time.Now()
					n, err := disp.handle(ctx, ev)
					if err != nil {
						st.fail(time.Since(t1), "%s dispatch %s: %v", c, ev.Type, err)
						continue
					}
					st.ok(time.Since(t1))
					mu.Lock()
					evCount[ev.Type]++
					alerts += n
					if ev.Type == events.StockLow {
						var p events.StockLowData
						_ = json.Unmarshal(ev.Data, &p)
						if lowSeen[c] == nil {
							lowSeen[c] = map[string]bool{}
						}
						lowSeen[c][p.Name] = true
						if p.Stock >= p.MinStock {
							st.fail(0, "%s stock.low for %s with stock %d >= min %d", c, p.Name, p.Stock, p.MinStock)
						}
					}
					mu.Unlock()
				}
			}
			if h%3 == 0 {
				r, d, err := e.say(c, "owner", fmt.Sprintf("Ahmet Yılmaz'dan %d TL tahsilat", 1000+h/3*100))
				if err != nil || !strings.Contains(r.text(), "Kaydedildi") {
					st.fail(d, "%s collection: %v %q", c, err, r.text())
				} else {
					st.ok(d)
				}
			}
		})
	}
	for i := range companies {
		c := domain.CompanyID(fmt.Sprintf("bd%03d", i))
		owner, _ := e.dev.Outbox(ctx, c, "owner")
		acc, _ := e.dev.Outbox(ctx, c, "accountant")
		seen := map[string]bool{}
		for _, m := range owner {
			if seen[m.Text] {
				st.fail(0, "%s owner got duplicate alert %q", c, m.Text)
			}
			seen[m.Text] = true
		}
		stockAlerts := 0
		for _, m := range acc {
			if strings.HasPrefix(m.Text, "⏰") {
				st.fail(0, "%s accountant got an overdue alert it did not subscribe to", c)
			}
			if strings.HasPrefix(m.Text, "⚠️ Kritik stok") {
				stockAlerts++
			}
		}
		if stockAlerts != len(lowSeen[c]) {
			st.fail(0, "%s: %d stock alerts for %d low products", c, stockAlerts, len(lowSeen[c]))
		}
		r, d, err := e.say(c, "owner", "kritik stok")
		if err != nil {
			st.fail(d, "%s low stock query: %v", c, err)
			continue
		}
		st.ok(d)
		for name := range lowSeen[c] {
			if !strings.Contains(r.text(), name) {
				st.fail(0, "%s: alerted %s but query says %q", c, name, r.text())
			}
		}
		r, d, err = e.say(c, "owner", "gün sonu özeti")
		if err != nil || !strings.Contains(r.text(), "Tahsilat: 4.600,00 TL") {
			st.fail(d, "%s summary after day: %v %q", c, err, r.text())
		} else {
			st.ok(d)
		}
	}
	res := st.report(t, "business_day", time.Since(start), map[string]any{
		"companies": companies, "hours": hours, "events_order": evCount[events.OrderCreated],
		"events_stock_low": evCount[events.StockLow], "events_overdue": evCount[events.InvoiceOverdue], "alerts_delivered": alerts,
	})
	if res["errors"].(int) != 0 {
		t.Fatalf("%d errors", res["errors"])
	}
	if evCount[events.StockLow] == 0 || evCount[events.InvoiceOverdue] == 0 || alerts == 0 {
		t.Fatalf("business day produced no alerts: %v alerts=%d", evCount, alerts)
	}
}
