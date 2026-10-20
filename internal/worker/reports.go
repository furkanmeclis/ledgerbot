package worker

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/riverqueue/river"

	"github.com/furkanmeclis/ledgerbot/internal/assistant"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

// reportsWorker runs every few minutes on the River leader only, finds reports
// whose local send time has passed in the current period, and sends each one
// once per user and period (ThrottleOnce makes this idempotent across retries).
type reportsWorker struct {
	river.WorkerDefaults[queue.ReportsTickArgs]
	d *Deps
}

func (w *reportsWorker) Timeout(*river.Job[queue.ReportsTickArgs]) time.Duration {
	return 4 * time.Minute
}

func (w *reportsWorker) Work(ctx context.Context, _ *river.Job[queue.ReportsTickArgs]) error {
	if w.d.Companies == nil {
		return nil
	}
	ids, err := w.d.Companies.List(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	for _, c := range ids {
		snap, err := w.d.Tenants.Get(ctx, c)
		if errors.Is(err, tenant.ErrUnknownCompany) || (err == nil && len(snap.Settings.Reports) == 0) {
			continue
		}
		if err != nil {
			w.d.Log.Warn("reports: settings", "company", c, "err", err)
			continue
		}
		jobs, err := DueReports(ctx, w.d, snap, now)
		if err != nil {
			w.d.Log.Warn("reports: build", "company", c, "err", err)
			continue
		}
		if err := w.d.Queue.EnqueueMany(ctx, jobs); err != nil {
			return err
		}
	}
	return nil
}

// DueReports returns outbound jobs for reports due at now.
func DueReports(ctx context.Context, d *Deps, snap *tenant.Snapshot, now time.Time) ([]river.JobArgs, error) {
	loc := assistant.Istanbul
	if tz := snap.Settings.Timezone; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	local := now.In(loc)
	lang := "tr"
	if strings.HasPrefix(snap.Settings.Locale, "en") {
		lang = "en"
	}
	c := snap.Settings.CompanyID
	active, err := activeChannels(ctx, d, c)
	if err != nil {
		return nil, err
	}
	var jobs []river.JobArgs
	for _, r := range snap.Settings.Reports {
		if !due(r, local) {
			continue
		}
		_, _, period := assistant.Period(r.Type, local)
		var text string
		if r.Type == assistant.ReportCashForecast {
			f, err := assistant.ForecastCash(ctx, d.Data, c, local, 30)
			if err != nil {
				return nil, err
			}
			t, risky := assistant.CashText(f, lang)
			if !risky {
				continue // only warn when cash may go negative
			}
			text = t
		}
		for _, u := range snap.UsersWithRoles(r.Roles) {
			ok, err := d.Store.ThrottleOnce(ctx, c, "report|"+r.Type+"|"+period+"|"+string(u.ID), 40*24*time.Hour)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			if text == "" {
				if text, err = assistant.BuildReport(ctx, d.Data, c, r.Type, lang, local); err != nil {
					return nil, err
				}
			}
			if out, found := preferredAddress(u, active); found {
				out.CompanyID, out.Text = c, text
				jobs = append(jobs, queue.OutboundArgs{Out: out})
			}
		}
	}
	return jobs, nil
}

// due reports whether the schedule's send time in the current period has passed.
func due(r tenant.Report, local time.Time) bool {
	hh, mm := 8, 30
	if h, m, found := strings.Cut(r.At, ":"); found {
		if v, err := strconv.Atoi(h); err == nil {
			hh = v
		}
		if v, err := strconv.Atoi(m); err == nil {
			mm = v
		}
	}
	at := time.Date(local.Year(), local.Month(), local.Day(), hh, mm, 0, 0, local.Location())
	if local.Before(at) {
		return false
	}
	switch r.Type {
	case assistant.ReportWeekly:
		wd := r.Weekday
		if wd < 1 || wd > 7 {
			wd = 5
		}
		return int(local.Weekday()) == wd%7
	case assistant.ReportCashForecast:
		return true // checked daily; sent only when risky
	case assistant.ReportMonthly:
		day := r.Day
		if day < 1 || day > 28 {
			day = 25
		}
		return local.Day() == day
	}
	return true
}

func activeChannels(ctx context.Context, d *Deps, c domain.CompanyID) (map[domain.ChannelKind]bool, error) {
	conns, err := d.Store.ListConnections(ctx, c)
	if err != nil {
		return nil, err
	}
	active := map[domain.ChannelKind]bool{domain.ChannelDev: true}
	for _, cn := range conns {
		if cn.Enabled {
			active[cn.Kind] = true
		}
	}
	return active, nil
}

func preferredAddress(u tenant.User, active map[domain.ChannelKind]bool) (domain.OutboundMessage, bool) {
	for _, ch := range alertChannelOrder {
		if addr, has := u.Addresses[ch]; has && active[ch] {
			return domain.OutboundMessage{Channel: ch, To: addr}, true
		}
	}
	return domain.OutboundMessage{}, false
}
