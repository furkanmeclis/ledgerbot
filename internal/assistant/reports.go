package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Report types (settings.reports[].type and the query_report capability).
const (
	ReportDaily   = "daily_digest"
	ReportWeekly  = "weekly_pnl"
	ReportMonthly = "monthly_vat"
)

// Istanbul is the default reporting time zone.
var Istanbul = mustLoc("Europe/Istanbul")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("TRT", 3*3600)
	}
	return l
}

// Period returns the [from, to) range a report covers when produced at now,
// plus a stable key used to send each period only once.
func Period(kind string, now time.Time) (from, to time.Time, key string) {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch kind {
	case ReportWeekly: // the current ISO week up to now
		wd := (int(day.Weekday()) + 6) % 7 // Monday = 0
		from = day.AddDate(0, 0, -wd)
		y, w := from.ISOWeek()
		return from, now, fmt.Sprintf("%d-W%02d", y, w)
	case ReportMonthly: // month to date
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		return from, now, from.Format("2006-01")
	default: // yesterday, for a morning digest
		from = day.AddDate(0, 0, -1)
		return from, day, from.Format("2006-01-02")
	}
}

// BuildReport renders a report. All numbers come from the data provider.
func BuildReport(ctx context.Context, data dataprovider.Provider, c domain.CompanyID, kind, lang string, now time.Time) (string, error) {
	from, to, _ := Period(kind, now)
	s, err := data.PeriodSummary(ctx, c, from, to)
	if err != nil {
		return "", err
	}
	span := from.Format("02.01") + "–" + to.Add(-time.Second).Format("02.01.2006")
	switch kind {
	case ReportWeekly:
		net := s.Sales - s.Expenses
		return T(lang, "report_weekly", span, s.Sales.String(), s.Orders, s.Expenses.String(), net.String(), s.Collections.String(), s.Payments.String()), nil
	case ReportMonthly:
		due := s.OutputVAT - s.InputVAT
		key := "report_vat_due"
		if due < 0 {
			key, due = "report_vat_carry", -due
		}
		return T(lang, "report_vat", span, s.OutputVAT.String(), s.InputVAT.String()) + "\n" + T(lang, key, due.String()), nil
	default:
		od, err := data.Overdue(ctx, c, 0)
		if err != nil {
			return "", err
		}
		var overdue domain.Money
		for _, it := range od {
			overdue += it.Amount
		}
		low, err := data.LowStock(ctx, c)
		if err != nil {
			return "", err
		}
		return T(lang, "report_daily", from.Format("02.01.2006"), s.Sales.String(), s.Orders, s.Collections.String(), s.Expenses.String(), overdue.String(), len(od), len(low)), nil
	}
}

func runReport(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	kind := c.Args["kind"]
	if !slices.Contains([]string{ReportDaily, ReportWeekly, ReportMonthly}, kind) {
		kind = ReportWeekly
	}
	text, err := BuildReport(ctx, e.Data, c.Actor.CompanyID, kind, c.Lang, e.now().In(Istanbul))
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Text: text}, nil
}

// allocate matches a collection to the customer's open invoices: an invoice with
// exactly this amount wins, otherwise oldest-first. The remainder is an advance.
func allocate(open []dataprovider.OverdueItem, amount domain.Money) ([]dataprovider.Allocation, domain.Money) {
	if len(open) == 0 {
		return nil, amount
	}
	for _, inv := range open {
		if inv.Amount == amount {
			return []dataprovider.Allocation{{InvoiceNo: inv.InvoiceNo, Amount: amount}}, 0
		}
	}
	open = slices.Clone(open)
	slices.SortStableFunc(open, func(a, b dataprovider.OverdueItem) int { return a.DueDate.Compare(b.DueDate) })
	var out []dataprovider.Allocation
	left := amount
	for _, inv := range open {
		if left <= 0 {
			break
		}
		pay := min(left, inv.Amount)
		out = append(out, dataprovider.Allocation{InvoiceNo: inv.InvoiceNo, Amount: pay})
		left -= pay
	}
	return out, left
}

func allocationNote(lang string, allocs []dataprovider.Allocation, open map[string]domain.Money, advance domain.Money) string {
	if len(allocs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, a := range allocs {
		key := "alloc_closed"
		if a.Amount < open[a.InvoiceNo] {
			key = "alloc_partial"
		}
		b.WriteString("\n" + T(lang, key, a.InvoiceNo, a.Amount.String()))
	}
	if advance > 0 {
		b.WriteString("\n" + T(lang, "alloc_advance", advance.String()))
	}
	return b.String()
}
