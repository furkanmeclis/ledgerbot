package worker

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider/fake"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func TestDue(t *testing.T) {
	fri := time.Date(2026, 10, 23, 17, 45, 0, 0, assistant.Istanbul) // Friday
	cases := []struct {
		r    tenant.Report
		at   time.Time
		want bool
	}{
		{tenant.Report{Type: assistant.ReportDaily, At: "08:30"}, fri, true},
		{tenant.Report{Type: assistant.ReportDaily, At: "18:00"}, fri, false},
		{tenant.Report{Type: assistant.ReportWeekly, At: "17:30", Weekday: 5}, fri, true},
		{tenant.Report{Type: assistant.ReportWeekly, At: "17:30", Weekday: 1}, fri, false},
		{tenant.Report{Type: assistant.ReportMonthly, At: "10:00", Day: 23}, fri, true},
		{tenant.Report{Type: assistant.ReportMonthly, At: "10:00", Day: 20}, fri, false},
	}
	for i, c := range cases {
		if got := due(c.r, c.at); got != c.want {
			t.Errorf("case %d: due=%v want %v", i, got, c.want)
		}
	}
}

func TestDueReportsOncePerPeriod(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	src := &tenant.FileSource{Dir: "../../seed/companies"}
	d := &Deps{Store: store.NewMem(), Data: fake.New(fake.NewMemLedger()), Log: log, Tenants: tenant.NewCache(src, time.Minute, log)}
	snap, err := d.Tenants.Get(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	fri := time.Date(2026, 10, 23, 18, 0, 0, 0, assistant.Istanbul)
	jobs, err := DueReports(context.Background(), d, snap, fri)
	if err != nil {
		t.Fatal(err)
	}
	// daily digest -> owner; weekly (Friday) -> owner + accountant; monthly not due (day 20).
	if len(jobs) != 3 {
		t.Fatalf("want 3 report messages, got %d", len(jobs))
	}
	for _, j := range jobs {
		if out := j.(queue.OutboundArgs).Out; out.Text == "" || out.To == "" {
			t.Fatalf("empty report message: %+v", out)
		}
	}
	again, _ := DueReports(context.Background(), d, snap, fri.Add(10*time.Minute))
	if len(again) != 0 {
		t.Fatalf("reports must be sent once per period, got %d", len(again))
	}
}
