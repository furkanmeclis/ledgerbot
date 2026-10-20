package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

// anomalies compares a new record with the company's committed history for
// the same counterparty: an amount far above the usual one, or the same amount
// again within a week (a likely duplicate that is not the same document).
func (e *Engine) anomalies(ctx context.Context, call *Call, capability string, rec dataprovider.Record) []string {
	if rec.Kind != dataprovider.RecordExpense && rec.Kind != dataprovider.RecordPayment && rec.Kind != dataprovider.RecordCollection {
		return nil
	}
	key := counterparty(rec)
	if key == "" {
		return nil
	}
	hist, err := e.Store.ListDrafts(ctx, call.Actor.CompanyID, store.DraftFilter{Status: domain.DraftCommitted, Limit: 200})
	if err != nil {
		return nil
	}
	var amounts []domain.Money
	var warn []string
	weekAgo := e.now().AddDate(0, 0, -7)
	for _, d := range hist {
		if d.Capability != capability {
			continue
		}
		var r dataprovider.Record
		if json.Unmarshal(d.Payload, &r) != nil || counterparty(r) != key {
			continue
		}
		amounts = append(amounts, r.Amount)
		if r.Amount == rec.Amount && d.CreatedAt.After(weekAgo) && len(warn) == 0 {
			warn = append(warn, T(call.Lang, "anomaly_repeat", d.CreatedAt.Format("02.01"), rec.Amount.String()))
		}
	}
	if len(amounts) >= 3 {
		slices.Sort(amounts)
		median := amounts[len(amounts)/2]
		if rec.Amount > 3*median && rec.Amount-median > 100_000 { // > 3× and > 1.000 TL above
			warn = append(warn, T(call.Lang, "anomaly_amount", rec.Amount.String(), median.String()))
		}
	}
	if len(warn) > 0 {
		_ = e.publish(ctx, call.Actor.CompanyID, events.AnomalyDetected, map[string]any{
			"capability": capability, "counterparty": key, "amount": rec.Amount, "warnings": warn, "at": time.Now().UTC(),
		})
	}
	return warn
}

func counterparty(r dataprovider.Record) string {
	switch {
	case r.CustomerID != "":
		return "c:" + r.CustomerID
	case r.VendorTax != "":
		return "t:" + r.VendorTax
	case r.Vendor != "":
		return "v:" + strings.ToLower(strings.TrimSpace(r.Vendor))
	}
	return ""
}
