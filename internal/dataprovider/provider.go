// Package dataprovider is the contract between ledgerbot and the accounting
// data it reads and writes. The assistant only talks to this interface; it does
// not know whether data comes from seed fixtures, the host REST API or a
// read-only database.
//
// Drivers:
//   - fake: deterministic seed data per company plus a ledger table, used for
//     development, demos, evals and load tests.
//   - api:  the host system's REST API (to be filled in by the host team).
package dataprovider

import (
	"context"
	"errors"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrNotFound is returned when a referenced entity does not exist.
var ErrNotFound = errors.New("dataprovider: not found")

// ErrNotImplemented is returned by drivers that do not support an operation yet.
var ErrNotImplemented = errors.New("dataprovider: not implemented")

// Customer is a cari (account) in the host system.
type Customer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// Balance is a customer's current balance. Positive means the customer owes us.
type Balance struct {
	CustomerID string       `json:"customer_id"`
	Balance    domain.Money `json:"balance"`
	Overdue    domain.Money `json:"overdue"`
	AsOf       time.Time    `json:"as_of"`
}

// Product is a stock item.
type Product struct {
	ID       string       `json:"id"`
	SKU      string       `json:"sku"`
	Name     string       `json:"name"`
	Unit     string       `json:"unit"`
	Price    domain.Money `json:"price"`
	VATRate  int          `json:"vat_rate"` // percent
	Stock    int64        `json:"stock"`
	MinStock int64        `json:"min_stock"`
}

// OverdueItem is an unpaid invoice past its due date.
type OverdueItem struct {
	CustomerID   string       `json:"customer_id"`
	CustomerName string       `json:"customer_name"`
	InvoiceNo    string       `json:"invoice_no"`
	Amount       domain.Money `json:"amount"`
	DueDate      time.Time    `json:"due_date"`
}

// Summary is a daily overview.
type Summary struct {
	Day         time.Time    `json:"day"`         // period start
	To          time.Time    `json:"to,omitzero"` // period end (exclusive); zero for a single day
	Sales       domain.Money `json:"sales"`       // gross, VAT included
	Collections domain.Money `json:"collections"`
	Payments    domain.Money `json:"payments"`
	Expenses    domain.Money `json:"expenses"`   // gross, VAT included
	OutputVAT   domain.Money `json:"output_vat"` // VAT on sales (hesaplanan KDV)
	InputVAT    domain.Money `json:"input_vat"`  // VAT on expenses (indirilecek KDV)
	Orders      int          `json:"orders"`
}

// Allocation closes (part of) an open invoice with a collection.
type Allocation struct {
	InvoiceNo string       `json:"invoice_no"`
	Amount    domain.Money `json:"amount"`
}

// RecordKind names a write operation.
type RecordKind string

const (
	RecordCollection RecordKind = "collection" // tahsilat
	RecordPayment    RecordKind = "payment"    // ödeme
	RecordExpense    RecordKind = "expense"    // gider / alış faturası
	RecordOrder      RecordKind = "order"      // B2B sipariş
	RecordCustom     RecordKind = "custom"     // produced by a plugin capability
)

// Line is an item line on an expense or order.
type Line struct {
	ProductID string       `json:"product_id,omitempty"`
	Name      string       `json:"name"`
	Quantity  int64        `json:"quantity"` // in thousandths of a unit (1000 = 1)
	Unit      string       `json:"unit,omitempty"`
	UnitPrice domain.Money `json:"unit_price"`
	VATRate   int          `json:"vat_rate"`
	Total     domain.Money `json:"total"` // gross line total, computed in code
}

// Record is a write request. Exactly the fields relevant to Kind are set.
type Record struct {
	Kind       RecordKind   `json:"kind"`
	CustomerID string       `json:"customer_id,omitempty"`
	Vendor     string       `json:"vendor,omitempty"`
	VendorTax  string       `json:"vendor_tax_id,omitempty"`
	DocumentNo string       `json:"document_no,omitempty"`
	Date       time.Time    `json:"date"`
	Method     string       `json:"method,omitempty"` // nakit, havale, kart, çek
	Amount     domain.Money `json:"amount"`
	VAT        domain.Money `json:"vat,omitempty"`
	Lines      []Line       `json:"lines,omitempty"`
	Note       string       `json:"note,omitempty"`
	// Allocations match a collection to open invoices (computed by ledgerbot).
	Allocations []Allocation  `json:"allocations,omitempty"`
	ActorID     domain.UserID `json:"actor_id"`
	DraftID     string        `json:"draft_id"` // idempotency key towards the host
}

// Provider is the data contract.
type Provider interface {
	FindCustomers(ctx context.Context, c domain.CompanyID, query string, limit int) ([]Customer, error)
	Balance(ctx context.Context, c domain.CompanyID, customerID string) (Balance, error)
	Overdue(ctx context.Context, c domain.CompanyID, limit int) ([]OverdueItem, error)
	FindProducts(ctx context.Context, c domain.CompanyID, query string, limit int) ([]Product, error)
	LowStock(ctx context.Context, c domain.CompanyID) ([]Product, error)
	DailySummary(ctx context.Context, c domain.CompanyID, day time.Time) (Summary, error)
	// PeriodSummary aggregates [from, to).
	PeriodSummary(ctx context.Context, c domain.CompanyID, from, to time.Time) (Summary, error)
	// OpenInvoices lists a customer's unpaid invoices, oldest due date first.
	OpenInvoices(ctx context.Context, c domain.CompanyID, customerID string) ([]OverdueItem, error)
	// Commit writes a record. It must be idempotent on Record.DraftID.
	Commit(ctx context.Context, c domain.CompanyID, r Record) (externalID string, err error)
}
