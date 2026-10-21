package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ReportCashForecast is a scheduled check that only sends when cash may go negative.
const ReportCashForecast = "cash_forecast"

// Forecast is a 30-day cash projection.
type Forecast struct {
	Start      domain.Money
	Low        domain.Money
	LowDate    time.Time
	FirstBelow time.Time // zero when the balance never goes negative
	Outflows   []dataprovider.Due
}

// ForecastCash projects the balance day by day from the provider's cash
// position and upcoming receivables and payables.
func ForecastCash(ctx context.Context, data dataprovider.Provider, c domain.CompanyID, now time.Time, days int) (Forecast, error) {
	start, err := data.CashPosition(ctx, c)
	if err != nil {
		return Forecast{}, err
	}
	dues, err := data.Upcoming(ctx, c, now.AddDate(0, 0, days))
	if err != nil {
		return Forecast{}, err
	}
	f := Forecast{Start: start, Low: start, LowDate: now}
	bal := start
	for _, d := range dues { // sorted by date
		bal += d.Amount
		if bal < f.Low {
			f.Low, f.LowDate = bal, d.Date
		}
		if bal < 0 && f.FirstBelow.IsZero() {
			f.FirstBelow = d.Date
		}
		if d.Amount < 0 {
			f.Outflows = append(f.Outflows, d)
		}
	}
	return f, nil
}

// CashText renders a forecast; risky reports whether cash goes negative.
func CashText(f Forecast, lang string) (text string, risky bool) {
	var b strings.Builder
	if !f.FirstBelow.IsZero() {
		b.WriteString(T(lang, "cash_risk", f.FirstBelow.Format("02.01.2006"), f.Low.String(), f.LowDate.Format("02.01")))
	} else {
		b.WriteString(T(lang, "cash_ok", f.Start.String(), f.Low.String(), f.LowDate.Format("02.01")))
	}
	n := 0
	for _, d := range f.Outflows {
		if n == 4 {
			break
		}
		fmt.Fprintf(&b, "\n• %s %s %s", d.Date.Format("02.01"), d.Description, (-d.Amount).String())
		n++
	}
	return b.String(), !f.FirstBelow.IsZero()
}

func runCashForecast(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	f, err := ForecastCash(ctx, e.Data, c.Actor.CompanyID, e.now(), 30)
	if err != nil {
		return Outcome{}, err
	}
	text, _ := CashText(f, c.Lang)
	return Outcome{Text: text}, nil
}
