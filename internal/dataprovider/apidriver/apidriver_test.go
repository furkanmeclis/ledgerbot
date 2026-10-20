package apidriver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
)

func TestDriver(t *testing.T) {
	var gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/companies/acme/customers":
			_ = json.NewEncoder(w).Encode([]dataprovider.Customer{{ID: "C1", Name: r.URL.Query().Get("q")}})
		case "/companies/acme/receivables/overdue":
			_, _ = w.Write([]byte(`[{"customer_id":"C1","invoice_no":"F1","amount":1000,"due_date":"2026-10-01"}]`))
		case "/companies/acme/records":
			gotIdem = r.Header.Get("Idempotency-Key")
			_, _ = w.Write([]byte(`{"external_id":"X-1"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	d := &Driver{Base: srv.URL, Token: "tok"}
	ctx := t.Context()
	cs, err := d.FindCustomers(ctx, "acme", "Ahmet", 5)
	if err != nil || len(cs) != 1 || cs[0].Name != "Ahmet" {
		t.Fatalf("customers: %+v %v", cs, err)
	}
	od, err := d.Overdue(ctx, "acme", 0)
	if err != nil || len(od) != 1 || od[0].DueDate.Format(time.DateOnly) != "2026-10-01" {
		t.Fatalf("overdue: %+v %v", od, err)
	}
	ext, err := d.Commit(ctx, "acme", dataprovider.Record{Kind: dataprovider.RecordCollection, DraftID: "drf_1", Amount: 100})
	if err != nil || ext != "X-1" || gotIdem != "drf_1" {
		t.Fatalf("commit: %s %v idem=%s", ext, err, gotIdem)
	}
	if _, err := d.Balance(ctx, "acme", "missing"); !errors.Is(err, dataprovider.ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}
