package simulation

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

// TestManyCompaniesMixedTraffic drives 500 companies (× LEDGERBOT_SIM_SCALE)
// concurrently through the HTTP API with a realistic message mix and checks
// that every number a company sees is computed from its own data only.
func TestManyCompaniesMixedTraffic(t *testing.T) {
	n := scaled(500)
	workers := runtime.GOMAXPROCS(0) * 8
	e := newSimEnv(t, simOpts{prefixes: map[string]domain.SaveMode{"mc": domain.SaveAuto}, conns: workers})
	ctx := context.Background()
	st := &stats{}
	leaks := &stats{}
	start := time.Now()
	pool(n, workers, func(i int) {
		c := domain.CompanyID(fmt.Sprintf("mc%04d", i))
		cust, err := e.data.FindCustomers(ctx, c, "Ahmet Yılmaz", 1)
		if err != nil || len(cust) == 0 {
			st.fail(0, "%s: customer lookup: %v", c, err)
			return
		}
		open, _ := e.data.Balance(ctx, c, cust[0].ID)
		coll := domain.Money(100_000 + int64(i)*100 + 17) // unique per company, in kuruş
		pay := domain.Money(50_000 + int64(i)*100 + 3)
		const order = domain.Money(44_400) // 2 × 185,00 TL + 20% VAT
		type step struct {
			sender, text string
			check        func(r reply) string
		}
		want := func(subs ...string) func(reply) string {
			return func(r reply) string {
				for _, s := range subs {
					if !strings.Contains(r.text(), s) {
						return fmt.Sprintf("missing %q in %q", s, r.text())
					}
				}
				return ""
			}
		}
		steps := []step{
			{"owner", "Ahmet Yılmaz'dan " + plain(coll) + " TL tahsilat", want("Kaydedildi", coll.String())},
			{"accountant", "Ahmet Yılmaz'a " + plain(pay) + " TL ödeme yaptım", want("Onaylıyor musunuz")},
			{"accountant", "evet", want("Kaydedildi", pay.String())},
			{"viewer", "stok toner", want("Toner 85A stok")},
			{"viewer", "vadesi geçen alacaklar", want("Vadesi geçen")},
			{"owner", "Ahmet Yılmaz için 2 paket A4 kağıdı sipariş oluştur", want("Kaydedildi", order.String())},
			{"viewer", "kritik stok", want("ritik")},
			{"owner", "gün sonu özeti", want("Tahsilat: "+coll.String()+"\n", "Ödeme: "+pay.String()+"\n", "Satış: "+order.String()+" (1 sipariş)")},
			{"owner", "Ahmet Yılmaz'ın bakiyesi", want(fmt.Sprintf("Ahmet Yılmaz bakiyesi: %s", open.Balance-coll+pay+order))},
			{"viewer", "Ahmet Yılmaz'dan 10 TL tahsilat", want("yetkiniz yok")},
		}
		for _, s := range steps {
			r, d, err := e.say(c, s.sender, s.text)
			if err != nil {
				st.fail(d, "%s %q: %v", c, s.text, err)
				continue
			}
			if msg := s.check(r); msg != "" {
				st.fail(d, "%s %q: %s", c, s.text, msg)
				continue
			}
			if strings.Contains(r.text(), "Bir hata oluştu") || strings.Contains(r.text(), "anlayamadım") {
				st.fail(d, "%s %q: %s", c, s.text, r.text())
				continue
			}
			st.ok(d)
		}
		code, raw, d, err := e.call(c, http.MethodGet, "/v1/drafts?limit=100", nil)
		if err != nil || code != 200 {
			st.fail(d, "%s drafts: %d %v", c, code, err)
			return
		}
		st.ok(d)
		if strings.Count(string(raw), `"company_id":"`+string(c)+`"`) != 3 || strings.Count(string(raw), `"company_id":`) != 3 {
			leaks.fail(0, "%s sees foreign or missing drafts: %s", c, raw)
		}
	})
	elapsed := time.Since(start)
	for i := range n {
		c := domain.CompanyID(fmt.Sprintf("mc%04d", i))
		list, _ := e.st.ListDrafts(ctx, c, store.DraftFilter{})
		for _, d := range list {
			if d.CompanyID != c {
				leaks.fail(0, "store returned %s draft for %s", d.CompanyID, c)
			}
		}
	}
	res := st.report(t, "many_companies_mixed", elapsed, map[string]any{"companies": n, "workers": workers, "leaks": leaks.errors()})
	if res["errors"].(int) != 0 {
		t.Fatalf("%d errors", res["errors"])
	}
	if leaks.errors() != 0 {
		t.Fatalf("%d cross-company leaks: %v", leaks.errors(), leaks.sample)
	}
}
