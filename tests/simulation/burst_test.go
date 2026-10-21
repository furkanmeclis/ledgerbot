package simulation

import (
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// TestSingleCompanyBurst floods one company far beyond its rate limit while
// other companies send normal traffic. The noisy company must be throttled
// with 429s and nobody else may be throttled or slowed down noticeably.
func TestSingleCompanyBurst(t *testing.T) {
	const rps, burst = 50, 50
	noisyN := scaled(3000)
	quiet, perQuiet := scaled(50), 20
	e := newSimEnv(t, simOpts{prefixes: map[string]domain.SaveMode{"bu": domain.SaveAuto}, rps: rps, burst: burst, conns: 128})
	noisy := domain.CompanyID("bu-noisy")
	var ok, limited atomic.Int64
	noisySt, quietSt := &stats{}, &stats{}
	var quietLat []time.Duration
	var qmu sync.Mutex
	start := time.Now()
	var wg sync.WaitGroup
	wg.Go(func() {
		pool(noisyN, 48, func(int) {
			code, raw, d, err := e.call(noisy, http.MethodPost, "/v1/dev/messages", map[string]string{"sender": "viewer", "text": "stok toner"})
			switch {
			case err != nil:
				noisySt.fail(d, "noisy: %v", err)
			case code == http.StatusTooManyRequests:
				limited.Add(1)
				noisySt.ok(d)
			case code == 200:
				ok.Add(1)
				noisySt.ok(d)
			default:
				noisySt.fail(d, "noisy: %d %s", code, raw)
			}
		})
	})
	wg.Go(func() {
		pool(quiet, quiet, func(i int) {
			c := domain.CompanyID(fmt.Sprintf("bu%03d", i))
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for j := range perQuiet {
				<-tick.C
				text := "stok toner"
				if j%4 == 0 {
					text = fmt.Sprintf("Ahmet Yılmaz'dan %d TL tahsilat", 100+j)
				}
				r, d, err := e.say(c, "owner", text)
				if err != nil {
					quietSt.fail(d, "%s: %v", c, err)
					continue
				}
				if len(r.Texts) == 0 {
					quietSt.fail(d, "%s: empty reply", c)
					continue
				}
				quietSt.ok(d)
				qmu.Lock()
				quietLat = append(quietLat, d)
				qmu.Unlock()
			}
		})
	})
	wg.Wait()
	elapsed := time.Since(start)
	slices.Sort(quietLat)
	allowed := float64(burst) + rps*elapsed.Seconds()*1.1 + 5
	noisyRes := noisySt.report(t, "burst_noisy_company", elapsed, map[string]any{"accepted": ok.Load(), "rate_limited": limited.Load(), "allowed_max": int(allowed)})
	quietRes := quietSt.report(t, "burst_quiet_companies", elapsed, map[string]any{"companies": quiet})
	if noisyRes["errors"].(int) != 0 || quietRes["errors"].(int) != 0 {
		t.Fatalf("errors: noisy %v quiet %v", noisyRes["errors"], quietRes["errors"])
	}
	if limited.Load() == 0 || float64(ok.Load()) > allowed {
		t.Fatalf("noisy company not limited: accepted %d (allowed ≤ %.0f), limited %d", ok.Load(), allowed, limited.Load())
	}
	if len(quietLat) != quiet*perQuiet {
		t.Fatalf("quiet companies got %d/%d answers", len(quietLat), quiet*perQuiet)
	}
	if p99 := quietRes["p99_ms"].(float64); p99 > 1000 {
		t.Fatalf("quiet companies starved: p99 %.1f ms", p99)
	}
}
