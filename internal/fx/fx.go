// Package fx converts foreign-currency amounts to TRY at the daily rate.
// The default source is the Central Bank of the Republic of Türkiye (TCMB)
// daily XML; rates are cached per day so each process fetches once a day.
package fx

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrUnknownCurrency is returned for currencies without a rate.
var ErrUnknownCurrency = errors.New("fx: unknown currency")

// Rate is a currency's TRY rate for a day.
type Rate struct {
	Currency string    `json:"currency"`
	TRY      float64   `json:"try"` // 1 unit of Currency = TRY
	Day      time.Time `json:"day"`
	Source   string    `json:"source"`
}

// Source returns all rates for "today" (the latest published day).
type Source interface {
	Rates(ctx context.Context) (map[string]Rate, error)
}

// Converter caches a source per calendar day.
type Converter struct {
	src Source

	mu    sync.Mutex
	day   string
	rates map[string]Rate
}

// NewConverter wraps a source.
func NewConverter(src Source) *Converter { return &Converter{src: src} }

// ToTRY converts amount (minor units of currency) to TRY minor units.
func (c *Converter) ToTRY(ctx context.Context, currency string, amount domain.Money) (domain.Money, Rate, error) {
	currency = Normalize(currency)
	if currency == "TRY" {
		return amount, Rate{Currency: "TRY", TRY: 1}, nil
	}
	rates, err := c.load(ctx)
	if err != nil {
		return 0, Rate{}, err
	}
	r, ok := rates[currency]
	if !ok {
		return 0, Rate{}, fmt.Errorf("%w: %s", ErrUnknownCurrency, currency)
	}
	return domain.Money(math.Round(float64(amount) * r.TRY)), r, nil
}

func (c *Converter) load(ctx context.Context) (map[string]Rate, error) {
	today := time.Now().Format("2006-01-02")
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.day == today && c.rates != nil {
		return c.rates, nil
	}
	r, err := c.src.Rates(ctx)
	if err != nil {
		if c.rates != nil {
			return c.rates, nil // serve yesterday's rates rather than fail
		}
		return nil, err
	}
	c.day, c.rates = today, r
	return r, nil
}

// Normalize maps spoken currency names and symbols to ISO codes.
func Normalize(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "tl", "try", "₺", "lira", "türk lirası":
		return "TRY"
	case "usd", "$", "dolar", "dollar", "amerikan doları":
		return "USD"
	case "eur", "€", "euro", "avro":
		return "EUR"
	case "gbp", "£", "sterlin", "pound":
		return "GBP"
	case "chf", "frank":
		return "CHF"
	case "sar", "riyal":
		return "SAR"
	}
	return strings.ToUpper(strings.TrimSpace(s))
}

// TCMB fetches https://www.tcmb.gov.tr/kurlar/today.xml (ForexSelling).
type TCMB struct {
	URL    string
	Client *http.Client
}

type tcmbDoc struct {
	Date       string `xml:"Date,attr"`
	Currencies []struct {
		Code        string `xml:"CurrencyCode,attr"`
		Unit        string `xml:"Unit"`
		ForexSell   string `xml:"ForexSelling"`
		BanknoteSel string `xml:"BanknoteSelling"`
	} `xml:"Currency"`
}

func (t TCMB) Rates(ctx context.Context) (map[string]Rate, error) {
	u := t.URL
	if u == "" {
		u = "https://www.tcmb.gov.tr/kurlar/today.xml"
	}
	cl := t.Client
	if cl == nil {
		cl = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fx: tcmb status %d", resp.StatusCode)
	}
	var doc tcmbDoc
	if err := xml.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	day, _ := time.Parse("01/02/2006", doc.Date)
	out := make(map[string]Rate, len(doc.Currencies))
	for _, c := range doc.Currencies {
		v := c.ForexSell
		if v == "" {
			v = c.BanknoteSel
		}
		rate, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil || rate <= 0 {
			continue
		}
		unit, _ := strconv.ParseFloat(strings.TrimSpace(c.Unit), 64)
		if unit <= 0 {
			unit = 1
		}
		out[c.Code] = Rate{Currency: c.Code, TRY: rate / unit, Day: day, Source: "TCMB"}
	}
	return out, nil
}

// Static is a fixed-rate source for tests and offline demos.
type Static map[string]float64

func (s Static) Rates(context.Context) (map[string]Rate, error) {
	out := make(map[string]Rate, len(s))
	for k, v := range s {
		out[k] = Rate{Currency: k, TRY: v, Day: time.Now().Truncate(24 * time.Hour), Source: "static"}
	}
	return out, nil
}
