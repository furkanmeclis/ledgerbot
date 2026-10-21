// Package apidriver implements dataprovider.Provider against the host system's
// REST API described in docs/contracts/host.openapi.yaml. The host team only has
// to expose those endpoints; no ledgerbot code changes are needed.
package apidriver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Driver talks to the host REST API.
type Driver struct {
	Base   string
	Token  string
	Client *http.Client
}

var _ dataprovider.Provider = (*Driver)(nil)

var defaultClient = &http.Client{Timeout: 10 * time.Second}

func (d *Driver) do(ctx context.Context, method, path string, q url.Values, body, out any, idem string) error {
	u := strings.TrimRight(d.Base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if d.Token != "" {
		req.Header.Set("Authorization", "Bearer "+d.Token)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	cl := d.Client
	if cl == nil {
		cl = defaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return dataprovider.ErrNotFound
	}
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("apidriver: %s %s: status %d: %s", method, path, resp.StatusCode, msg)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func cpath(c domain.CompanyID, rest string) string {
	return "/companies/" + url.PathEscape(string(c)) + rest
}

func limitQ(q url.Values, limit int) url.Values {
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return q
}

func (d *Driver) FindCustomers(ctx context.Context, c domain.CompanyID, query string, limit int) ([]dataprovider.Customer, error) {
	var out []dataprovider.Customer
	err := d.do(ctx, http.MethodGet, cpath(c, "/customers"), limitQ(url.Values{"q": {query}}, limit), nil, &out, "")
	return out, err
}

func (d *Driver) Balance(ctx context.Context, c domain.CompanyID, id string) (dataprovider.Balance, error) {
	var out dataprovider.Balance
	err := d.do(ctx, http.MethodGet, cpath(c, "/customers/"+url.PathEscape(id)+"/balance"), nil, nil, &out, "")
	return out, err
}

func (d *Driver) Overdue(ctx context.Context, c domain.CompanyID, limit int) ([]dataprovider.OverdueItem, error) {
	var raw []struct {
		dataprovider.OverdueItem
		Due string `json:"due_date"`
	}
	if err := d.do(ctx, http.MethodGet, cpath(c, "/receivables/overdue"), limitQ(url.Values{}, limit), nil, &raw, ""); err != nil {
		return nil, err
	}
	out := make([]dataprovider.OverdueItem, len(raw))
	for i, r := range raw {
		out[i] = r.OverdueItem
		out[i].DueDate, _ = time.Parse("2006-01-02", r.Due)
	}
	return out, nil
}

func (d *Driver) FindProducts(ctx context.Context, c domain.CompanyID, query string, limit int) ([]dataprovider.Product, error) {
	var out []dataprovider.Product
	err := d.do(ctx, http.MethodGet, cpath(c, "/products"), limitQ(url.Values{"q": {query}}, limit), nil, &out, "")
	return out, err
}

func (d *Driver) LowStock(ctx context.Context, c domain.CompanyID) ([]dataprovider.Product, error) {
	var out []dataprovider.Product
	err := d.do(ctx, http.MethodGet, cpath(c, "/products"), url.Values{"low_stock": {"true"}}, nil, &out, "")
	return out, err
}

func (d *Driver) DailySummary(ctx context.Context, c domain.CompanyID, day time.Time) (dataprovider.Summary, error) {
	var raw struct {
		dataprovider.Summary
		Day string `json:"day"`
	}
	err := d.do(ctx, http.MethodGet, cpath(c, "/summary"), url.Values{"day": {day.Format("2006-01-02")}}, nil, &raw, "")
	raw.Summary.Day = day
	return raw.Summary, err
}

func (d *Driver) PeriodSummary(ctx context.Context, c domain.CompanyID, from, to time.Time) (dataprovider.Summary, error) {
	var raw struct {
		dataprovider.Summary
		Day string `json:"day"`
		To  string `json:"to"`
	}
	err := d.do(ctx, http.MethodGet, cpath(c, "/summary"), url.Values{"from": {from.Format("2006-01-02")}, "to": {to.Format("2006-01-02")}}, nil, &raw, "")
	raw.Summary.Day, raw.Summary.To = from, to
	return raw.Summary, err
}

func (d *Driver) OpenInvoices(ctx context.Context, c domain.CompanyID, customerID string) ([]dataprovider.OverdueItem, error) {
	var raw []struct {
		dataprovider.OverdueItem
		Due string `json:"due_date"`
	}
	if err := d.do(ctx, http.MethodGet, cpath(c, "/customers/"+url.PathEscape(customerID)+"/invoices/open"), nil, nil, &raw, ""); err != nil {
		return nil, err
	}
	out := make([]dataprovider.OverdueItem, len(raw))
	for i, r := range raw {
		out[i] = r.OverdueItem
		out[i].DueDate, _ = time.Parse("2006-01-02", r.Due)
	}
	return out, nil
}

func (d *Driver) CashPosition(ctx context.Context, c domain.CompanyID) (domain.Money, error) {
	var out struct {
		Balance domain.Money `json:"balance"`
	}
	err := d.do(ctx, http.MethodGet, cpath(c, "/cash"), nil, nil, &out, "")
	return out.Balance, err
}

func (d *Driver) Upcoming(ctx context.Context, c domain.CompanyID, until time.Time) ([]dataprovider.Due, error) {
	var raw []struct {
		Date        string       `json:"date"`
		Amount      domain.Money `json:"amount"`
		Description string       `json:"description"`
	}
	if err := d.do(ctx, http.MethodGet, cpath(c, "/cash/upcoming"), url.Values{"until": {until.Format("2006-01-02")}}, nil, &raw, ""); err != nil {
		return nil, err
	}
	out := make([]dataprovider.Due, len(raw))
	for i, r := range raw {
		out[i].Date, _ = time.Parse("2006-01-02", r.Date)
		out[i].Amount, out[i].Description = r.Amount, r.Description
	}
	return out, nil
}

func (d *Driver) Commit(ctx context.Context, c domain.CompanyID, r dataprovider.Record) (string, error) {
	var out struct {
		ExternalID string `json:"external_id"`
	}
	err := d.do(ctx, http.MethodPost, cpath(c, "/records"), nil, r, &out, r.DraftID)
	return out.ExternalID, err
}
