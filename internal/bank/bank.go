// Package bank parses bank statement exports (CSV and Excel) into
// transactions. Turkish banks export many layouts; columns are detected from
// header names (Tarih/Date, Açıklama/Description, Tutar/Amount or Borç/Alacak,
// Bakiye/Balance) instead of fixed positions. Amounts are parsed with the
// shared Turkish money parser, never with floats.
package bank

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrNoHeader is returned when no date and amount columns can be found.
var ErrNoHeader = errors.New("bank: statement header not recognized")

// Tx is one statement line. Amount is positive for money in (alacak), negative
// for money out (borç).
type Tx struct {
	Date        time.Time    `json:"date"`
	Description string       `json:"description"`
	Amount      domain.Money `json:"amount"`
	Balance     domain.Money `json:"balance,omitempty"`
	Reference   string       `json:"reference,omitempty"`
}

// Looks reports whether a file looks like a statement export by MIME or name.
func Looks(mime, filename, caption string) bool {
	n := strings.ToLower(filename + " " + caption)
	switch {
	case strings.Contains(mime, "csv"), strings.HasSuffix(n, ".csv"):
		return true
	case strings.Contains(mime, "spreadsheet"), strings.Contains(mime, "excel"), strings.HasSuffix(strings.TrimSpace(n), ".xlsx"):
		return true
	}
	for _, k := range []string{"ekstre", "hesap hareket", "statement", "banka"} {
		if strings.Contains(n, k) {
			return true
		}
	}
	return false
}

// Parse reads CSV (comma or semicolon, UTF-8 or Windows-1254 friendly) or XLSX.
func Parse(data []byte, mime, filename string) ([]Tx, error) {
	if isXLSX(data, mime, filename) {
		return parseXLSX(data)
	}
	return parseCSV(data)
}

func isXLSX(data []byte, mime, filename string) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || strings.Contains(mime, "spreadsheetml") || strings.HasSuffix(strings.ToLower(filename), ".xlsx")
}

func parseXLSX(data []byte) ([]Tx, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, ErrNoHeader
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, err
	}
	return fromRows(rows)
}

func parseCSV(data []byte) ([]Tx, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		data = cp1254ToUTF8(data)
	}
	sep := ','
	head := data[:min(len(data), 4096)] // preamble lines may have no separators
	if bytes.Count(head, []byte(";")) > bytes.Count(head, []byte(",")) {
		sep = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = sep, -1, true
	var rows [][]string
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, rec)
	}
	return fromRows(rows)
}

type columns struct{ date, desc, amount, debit, credit, balance, ref int }

func detect(row []string) (columns, bool) {
	c := columns{-1, -1, -1, -1, -1, -1, -1}
	for i, h := range row {
		h = strings.ToLower(strings.TrimSpace(h))
		switch {
		case c.date < 0 && (strings.Contains(h, "tarih") || h == "date" || strings.Contains(h, "işlem tarihi")):
			c.date = i
		case c.desc < 0 && (strings.Contains(h, "açıklama") || strings.Contains(h, "aciklama") || strings.Contains(h, "description") || strings.Contains(h, "işlem")):
			c.desc = i
		case strings.Contains(h, "bakiye") || strings.Contains(h, "balance"):
			c.balance = i
		case strings.Contains(h, "borç") || strings.Contains(h, "borc") || strings.Contains(h, "debit"):
			c.debit = i
		case strings.Contains(h, "alacak") || strings.Contains(h, "credit"):
			c.credit = i
		case strings.Contains(h, "tutar") || h == "amount":
			c.amount = i
		case strings.Contains(h, "dekont") || strings.Contains(h, "referans") || strings.Contains(h, "reference"):
			c.ref = i
		}
	}
	ok := c.date >= 0 && (c.amount >= 0 || c.debit >= 0 || c.credit >= 0)
	return c, ok
}

func fromRows(rows [][]string) ([]Tx, error) {
	var cols columns
	start := -1
	for i, row := range rows {
		if i > 30 {
			break
		}
		if c, ok := detect(row); ok {
			cols, start = c, i+1
			break
		}
	}
	if start < 0 {
		return nil, ErrNoHeader
	}
	cell := func(row []string, i int) string {
		if i < 0 || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}
	var out []Tx
	for _, row := range rows[start:] {
		d, ok := parseDate(cell(row, cols.date))
		if !ok {
			continue // totals, blank lines, footers
		}
		tx := Tx{Date: d, Description: cell(row, cols.desc), Reference: cell(row, cols.ref)}
		switch {
		case cols.amount >= 0:
			tx.Amount = signed(cell(row, cols.amount))
		default:
			tx.Amount = unsigned(cell(row, cols.credit)) - unsigned(cell(row, cols.debit))
		}
		if tx.Amount == 0 {
			continue
		}
		tx.Balance = signed(cell(row, cols.balance))
		out = append(out, tx)
	}
	return out, nil
}

func signed(s string) domain.Money {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	neg := strings.HasPrefix(s, "-") || strings.HasPrefix(s, "(") || strings.HasSuffix(s, "-")
	s = strings.Trim(s, "-+()")
	m, err := domain.ParseAmount(s)
	if err != nil {
		return 0
	}
	if neg {
		return -m
	}
	return m
}

func unsigned(s string) domain.Money {
	m := signed(s)
	if m < 0 {
		return -m
	}
	return m
}

var dateLayouts = []string{"02.01.2006", "02/01/2006", "2006-01-02", "02.01.2006 15:04", "02.01.2006 15:04:05", "02/01/2006 15:04", "2006-01-02 15:04:05", "01-02-06", "2.1.2006"}

func parseDate(s string) (time.Time, bool) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// cp1254ToUTF8 converts the Turkish Windows code page used by older exports.
func cp1254ToUTF8(b []byte) []byte {
	special := map[byte]rune{0xD0: 'Ğ', 0xDD: 'İ', 0xDE: 'Ş', 0xF0: 'ğ', 0xFD: 'ı', 0xFE: 'ş', 0x80: '€'}
	var out bytes.Buffer
	out.Grow(len(b) + len(b)/4)
	for _, c := range b {
		if r, ok := special[c]; ok {
			out.WriteRune(r)
		} else if c < 0x80 {
			out.WriteByte(c)
		} else {
			out.WriteRune(rune(c)) // Latin-1 compatible range (ç, ö, ü, Ç, Ö, Ü)
		}
	}
	return out.Bytes()
}
