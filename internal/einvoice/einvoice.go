// Package einvoice turns GİB UBL-TR e-Fatura / e-Arşiv XML into ledgerbot's
// line-item model using github.com/furkanmeclis/go-ubltr. No AI is involved:
// structured invoices are parsed deterministically.
package einvoice

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	ubltr "github.com/furkanmeclis/go-ubltr"
	"github.com/furkanmeclis/go-ubltr/schema/ubltr121/cac"
	"github.com/furkanmeclis/go-ubltr/schema/ubltr121/invoice"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrNotInvoice is returned for XML that is not a UBL-TR invoice.
var ErrNotInvoice = errors.New("einvoice: not a UBL-TR invoice")

// Party is a supplier or customer.
type Party struct {
	Name  string `json:"name"`
	TaxID string `json:"tax_id,omitempty"` // VKN or TCKN
}

// Document is the parsed invoice.
type Document struct {
	ID        string              `json:"id"`
	UUID      string              `json:"uuid"`
	Profile   string              `json:"profile"`
	Type      string              `json:"type"`
	IssueDate time.Time           `json:"issue_date"`
	Currency  string              `json:"currency"`
	Supplier  Party               `json:"supplier"`
	Customer  Party               `json:"customer"`
	Lines     []dataprovider.Line `json:"lines"`
	Net       domain.Money        `json:"net"`
	VAT       domain.Money        `json:"vat"`
	Payable   domain.Money        `json:"payable"`
}

// Looks reports whether data is probably UBL XML (cheap check before parsing).
func Looks(data []byte) bool {
	head := data[:min(len(data), 1024)]
	return bytes.Contains(head, []byte("<?xml")) || bytes.Contains(head, []byte("Invoice"))
}

// Parse reads an invoice.
func Parse(data []byte) (Document, error) {
	kind, err := ubltr.Detect(data)
	if err != nil {
		return Document{}, fmt.Errorf("%w: %w", ErrNotInvoice, err)
	}
	if kind != ubltr.KindInvoice {
		return Document{}, ErrNotInvoice
	}
	var inv invoice.Invoice
	if err := ubltr.Unmarshal(data, &inv); err != nil {
		return Document{}, fmt.Errorf("einvoice: unmarshal: %w", err)
	}
	d := Document{
		ID:       inv.ID.Value,
		UUID:     inv.UUID.Value,
		Profile:  inv.ProfileID.Value,
		Type:     inv.InvoiceTypeCode.Value,
		Currency: inv.DocumentCurrencyCode.Value,
		Supplier: party(inv.AccountingSupplierParty.Party),
		Customer: party(inv.AccountingCustomerParty.Party),
		Net:      Decimal(inv.LegalMonetaryTotal.TaxExclusiveAmount.Value),
		Payable:  Decimal(inv.LegalMonetaryTotal.PayableAmount.Value),
	}
	d.IssueDate, _ = time.Parse("2006-01-02", strings.TrimSpace(inv.IssueDate.Value))
	for _, t := range inv.TaxTotal {
		d.VAT += Decimal(t.TaxAmount.Value)
	}
	d.Lines = make([]dataprovider.Line, 0, len(inv.InvoiceLine))
	for _, l := range inv.InvoiceLine {
		line := dataprovider.Line{
			Name:      strings.TrimSpace(l.Item.Name.Value),
			Quantity:  Quantity(l.InvoicedQuantity.Value),
			Unit:      unitName(l.InvoicedQuantity.UnitCode),
			UnitPrice: Decimal(l.Price.PriceAmount.Value),
		}
		net := Decimal(l.LineExtensionAmount.Value)
		var tax domain.Money
		if l.TaxTotal != nil {
			tax = Decimal(l.TaxTotal.TaxAmount.Value)
			for _, st := range l.TaxTotal.TaxSubtotal {
				if st.Percent != nil {
					if p, err := strconv.ParseFloat(strings.TrimSpace(st.Percent.Value), 64); err == nil {
						line.VATRate = int(math.Round(p))
						break
					}
				}
			}
		}
		line.Total = net + tax
		d.Lines = append(d.Lines, line)
	}
	return d, nil
}

func party(p cac.PartyType) Party {
	out := Party{}
	if p.PartyName != nil {
		out.Name = strings.TrimSpace(p.PartyName.Name.Value)
	}
	if out.Name == "" && p.Person != nil {
		out.Name = strings.TrimSpace(p.Person.FirstName.Value + " " + p.Person.FamilyName.Value)
	}
	for _, id := range p.PartyIdentification {
		if s := strings.ToUpper(id.ID.SchemeID); s == "VKN" || s == "TCKN" {
			out.TaxID = strings.TrimSpace(id.ID.Value)
			break
		}
	}
	return out
}

// Decimal reads a dot-decimal UBL amount into minor units, rounding half away from zero.
func Decimal(s string) domain.Money {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return domain.Money(math.Round(f * 100))
}

// Quantity reads a UBL quantity into thousandths.
func Quantity(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return int64(math.Round(f * 1000))
}

// unitName maps common UN/ECE rec 20 codes used by GİB to Turkish names.
func unitName(code string) string {
	switch code {
	case "C62", "NIU", "H87":
		return "adet"
	case "KGM":
		return "kg"
	case "LTR":
		return "litre"
	case "MTR":
		return "metre"
	case "MTK":
		return "m²"
	case "BX":
		return "kutu"
	case "PA":
		return "paket"
	case "SET":
		return "takım"
	case "":
		return ""
	}
	return code
}
