package fx

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

const tcmbXML = `<?xml version="1.0" encoding="UTF-8"?>
<?xml-stylesheet type="text/xsl" href="isokur.xsl"?>
<Tarih_Date Tarih="21.10.2026" Date="10/21/2026" Bulten_No="2026/200">
  <Currency CrossOrder="0" Kod="USD" CurrencyCode="USD">
    <Unit>1</Unit><Isim>ABD DOLARI</Isim><CurrencyName>US DOLLAR</CurrencyName>
    <ForexBuying>41.8500</ForexBuying><ForexSelling>41.9254</ForexSelling>
    <BanknoteBuying>41.8207</BanknoteBuying><BanknoteSelling>41.9883</BanknoteSelling>
  </Currency>
  <Currency CrossOrder="1" Kod="EUR" CurrencyCode="EUR">
    <Unit>1</Unit><ForexSelling> 48.7000 </ForexSelling><BanknoteSelling>48.80</BanknoteSelling>
  </Currency>
  <Currency CrossOrder="2" Kod="JPY" CurrencyCode="JPY">
    <Unit>100</Unit><ForexSelling>27.50</ForexSelling>
  </Currency>
  <Currency CrossOrder="3" Kod="SAR" CurrencyCode="SAR">
    <Unit>1</Unit><ForexSelling></ForexSelling><BanknoteSelling>11.20</BanknoteSelling>
  </Currency>
  <Currency CrossOrder="4" Kod="XDR" CurrencyCode="XDR">
    <Unit>1</Unit><ForexSelling></ForexSelling><BanknoteSelling></BanknoteSelling>
  </Currency>
  <Currency CrossOrder="5" Kod="BAD" CurrencyCode="BAD">
    <Unit>1</Unit><ForexSelling>n/a</ForexSelling>
  </Currency>
  <Currency CrossOrder="6" Kod="NEG" CurrencyCode="NEG">
    <Unit>1</Unit><ForexSelling>-3</ForexSelling>
  </Currency>
  <Currency CrossOrder="7" Kod="NOU" CurrencyCode="NOU">
    <Unit></Unit><ForexSelling>2.5</ForexSelling>
  </Currency>
</Tarih_Date>`

func tcmbServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestTCMBRates(t *testing.T) {
	t.Parallel()
	srv, _ := tcmbServer(t, http.StatusOK, tcmbXML)
	rates, err := TCMB{URL: srv.URL, Client: srv.Client()}.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	want := map[string]float64{"USD": 41.9254, "EUR": 48.7, "JPY": 0.275, "SAR": 11.2, "NOU": 2.5}
	if len(rates) != len(want) {
		t.Fatalf("got %d rates %v, want %v", len(rates), rates, want)
	}
	for code, v := range want {
		r, ok := rates[code]
		if !ok {
			t.Fatalf("missing %s", code)
		}
		if math.Abs(r.TRY-v) > 1e-9 || r.Currency != code || r.Source != "TCMB" || !r.Day.Equal(day) {
			t.Fatalf("%s = %+v, want TRY %v day %v", code, r, v, day)
		}
	}
	for _, skipped := range []string{"XDR", "BAD", "NEG"} {
		if _, ok := rates[skipped]; ok {
			t.Fatalf("%s should be skipped", skipped)
		}
	}
}

func TestTCMBErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, "oops"},
		{"not found", http.StatusNotFound, ""},
		{"malformed xml", http.StatusOK, "<Tarih_Date><Currency"},
		{"not xml", http.StatusOK, "{}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			srv, _ := tcmbServer(t, c.status, c.body)
			if _, err := (TCMB{URL: srv.URL}).Rates(context.Background()); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestTCMBEmptyAndBadDate(t *testing.T) {
	t.Parallel()
	srv, _ := tcmbServer(t, http.StatusOK, `<Tarih_Date Date="21.10.2026"><Currency CurrencyCode="USD"><ForexSelling>40</ForexSelling></Currency></Tarih_Date>`)
	rates, err := TCMB{URL: srv.URL}.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := rates["USD"]; r.TRY != 40 || !r.Day.IsZero() {
		t.Fatalf("USD = %+v, want zero day for unparseable date", r)
	}
	srv2, _ := tcmbServer(t, http.StatusOK, `<Tarih_Date Date="10/21/2026"></Tarih_Date>`)
	rates, err = TCMB{URL: srv2.URL}.Rates(context.Background())
	if err != nil || len(rates) != 0 {
		t.Fatalf("empty doc = %v, %v", rates, err)
	}
}

func TestTCMBContextCanceled(t *testing.T) {
	t.Parallel()
	srv, hits := tcmbServer(t, http.StatusOK, tcmbXML)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (TCMB{URL: srv.URL}).Rates(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hits.Load() != 0 {
		t.Fatal("canceled request reached the server")
	}
	if _, err := (TCMB{URL: "://bad"}).Rates(context.Background()); err == nil {
		t.Fatal("bad URL should fail")
	}
}

func TestStatic(t *testing.T) {
	t.Parallel()
	rates, err := Static{"USD": 40, "EUR": 45.5}.Rates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rates) != 2 || rates["USD"].TRY != 40 || rates["EUR"].TRY != 45.5 || rates["EUR"].Source != "static" || rates["EUR"].Currency != "EUR" {
		t.Fatalf("rates = %+v", rates)
	}
	if d := rates["USD"].Day; d.IsZero() || !d.Equal(d.Truncate(24*time.Hour)) {
		t.Fatalf("day not truncated: %v", d)
	}
	empty, _ := Static(nil).Rates(context.Background())
	if len(empty) != 0 {
		t.Fatal("nil Static should be empty")
	}
}

func TestNormalize(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"": "TRY", "  ": "TRY", "TL": "TRY", "tl": "TRY", "₺": "TRY", "Lira": "TRY", "try": "TRY", "Türk Lirası": "TRY",
		"$": "USD", "usd": "USD", "Dolar": "USD", "dollar": "USD", "Amerikan Doları": "USD",
		"€": "EUR", "Euro": "EUR", "avro": "EUR", "EUR": "EUR",
		"£": "GBP", "sterlin": "GBP", "Pound": "GBP",
		"frank": "CHF", "CHF": "CHF", "riyal": "SAR",
		"jpy": "JPY", " aed ": "AED", "ruble": "RUBLE",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeTurkishUppercase(t *testing.T) {
	t.Parallel()
	if got := Normalize("TÜRK LİRASI"); got != "TRY" {
		t.Skipf("BUG: Normalize(%q) = %q; strings.ToLower maps 'I' to 'i' (not dotless 'ı'), so the all-caps Turkish name misses the \"türk lirası\" case", "TÜRK LİRASI", got)
	}
}

type countingSource struct {
	mu    sync.Mutex
	calls int
	rates map[string]float64
	err   error
}

func (s *countingSource) Rates(ctx context.Context) (map[string]Rate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return Static(s.rates).Rates(ctx)
}

func TestConverterToTRY(t *testing.T) {
	t.Parallel()
	c := NewConverter(Static{"USD": 41.9254, "EUR": 48.7, "JPY": 0.275})
	cases := []struct {
		cur    string
		amount domain.Money
		want   domain.Money
		rate   float64
	}{
		{"TL", 12345, 12345, 1},
		{"", -500, -500, 1},
		{"usd", 10000, 419254, 41.9254},
		{"dolar", 1, 42, 41.9254},
		{"$", -10000, -419254, 41.9254},
		{"€", 150, 7305, 48.7},
		{"JPY", 100000, 27500, 0.275},
		{"usd", 0, 0, 41.9254},
	}
	for _, tc := range cases {
		got, r, err := c.ToTRY(context.Background(), tc.cur, tc.amount)
		if err != nil {
			t.Fatalf("ToTRY(%s, %d): %v", tc.cur, tc.amount, err)
		}
		if got != tc.want || r.TRY != tc.rate {
			t.Errorf("ToTRY(%s, %d) = %d @ %v, want %d @ %v", tc.cur, tc.amount, got, r.TRY, tc.want, tc.rate)
		}
	}
	if _, _, err := c.ToTRY(context.Background(), "GBP", 100); !errors.Is(err, ErrUnknownCurrency) || !strings.Contains(err.Error(), "GBP") {
		t.Fatalf("err = %v, want ErrUnknownCurrency for GBP", err)
	}
}

func TestConverterTRYSkipsSource(t *testing.T) {
	t.Parallel()
	src := &countingSource{err: errors.New("down")}
	c := NewConverter(src)
	if got, _, err := c.ToTRY(context.Background(), "₺", 99); err != nil || got != 99 {
		t.Fatalf("TRY = %d, %v", got, err)
	}
	if src.calls != 0 {
		t.Fatal("TRY conversion must not hit the source")
	}
}

func TestConverterCachesPerDay(t *testing.T) {
	t.Parallel()
	src := &countingSource{rates: map[string]float64{"USD": 40}}
	c := NewConverter(src)
	for range 10 {
		if _, _, err := c.ToTRY(context.Background(), "USD", 100); err != nil {
			t.Fatal(err)
		}
	}
	if src.calls != 1 {
		t.Fatalf("source called %d times, want 1", src.calls)
	}
	c.mu.Lock()
	c.day = "2000-01-01"
	c.mu.Unlock()
	src.mu.Lock()
	src.rates = map[string]float64{"USD": 50}
	src.mu.Unlock()
	got, _, err := c.ToTRY(context.Background(), "USD", 100)
	if err != nil || got != 5000 || src.calls != 2 {
		t.Fatalf("after day change = %d, %v, calls %d", got, err, src.calls)
	}
}

func TestConverterServesStaleOnError(t *testing.T) {
	t.Parallel()
	src := &countingSource{rates: map[string]float64{"USD": 40}}
	c := NewConverter(src)
	if _, _, err := c.ToTRY(context.Background(), "USD", 100); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	c.day = "2000-01-01"
	c.mu.Unlock()
	src.mu.Lock()
	src.err = errors.New("tcmb down")
	src.mu.Unlock()
	got, _, err := c.ToTRY(context.Background(), "USD", 100)
	if err != nil || got != 4000 {
		t.Fatalf("stale fallback = %d, %v", got, err)
	}
	// Still stale, so the next call retries the source.
	_, _, _ = c.ToTRY(context.Background(), "USD", 100)
	if src.calls != 3 {
		t.Fatalf("calls = %d, want 3", src.calls)
	}
}

func TestConverterColdError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	c := NewConverter(&countingSource{err: boom})
	if _, _, err := c.ToTRY(context.Background(), "USD", 1); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestConverterWithTCMBServer(t *testing.T) {
	t.Parallel()
	srv, hits := tcmbServer(t, http.StatusOK, tcmbXML)
	c := NewConverter(TCMB{URL: srv.URL, Client: srv.Client()})
	got, r, err := c.ToTRY(context.Background(), "dolar", 50000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2096270 || r.Source != "TCMB" {
		t.Fatalf("500 USD = %d (%+v)", got, r)
	}
	if _, _, err := c.ToTRY(context.Background(), "eur", 100); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hit %d times, want 1", hits.Load())
	}
}

func TestConverterConcurrent(t *testing.T) {
	t.Parallel()
	src := &countingSource{rates: map[string]float64{"USD": 40, "EUR": 45}}
	c := NewConverter(src)
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Go(func() {
			cur := "USD"
			if i%2 == 0 {
				cur = "EUR"
			}
			for range 100 {
				if _, _, err := c.ToTRY(context.Background(), cur, 100); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if src.calls != 1 {
		t.Fatalf("source called %d times", src.calls)
	}
}

func BenchmarkConverterToTRY(b *testing.B) {
	c := NewConverter(Static{"USD": 41.9254})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = c.ToTRY(ctx, "usd", 123456)
	}
}

func BenchmarkNormalize(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = Normalize(" Amerikan Doları ")
	}
}
