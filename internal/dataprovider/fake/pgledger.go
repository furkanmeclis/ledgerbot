package fake

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// PGLedger stores fake writes in PostgreSQL so every replica shares them.
type PGLedger struct{ DB *pgxpool.Pool }

func (l PGLedger) Append(ctx context.Context, draftID string, es []Entry) (bool, error) {
	tx, err := l.DB.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO fake_commits (draft_id) VALUES ($1) ON CONFLICT DO NOTHING`, draftID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	rows := make([][]any, len(es))
	for i, e := range es {
		rows[i] = []any{e.DraftID, string(e.CompanyID), string(e.Kind), e.CustomerID, e.ProductID, int64(e.Amount), int64(e.VAT), e.Quantity, e.Ref, e.At}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"fake_ledger"},
		[]string{"draft_id", "company_id", "kind", "customer_id", "product_id", "amount", "vat", "quantity", "ref", "at"},
		pgx.CopyFromRows(rows)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (l PGLedger) Totals(ctx context.Context, c domain.CompanyID) (Totals, error) {
	t := newTotals()
	rows, err := l.DB.Query(ctx, `
		SELECT kind, customer_id, product_id, ref, SUM(amount), SUM(quantity)
		FROM fake_ledger WHERE company_id = $1
		GROUP BY kind, customer_id, product_id, ref`, string(c))
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, cust, prod, ref string
		var amt, qty int64
		if err := rows.Scan(&kind, &cust, &prod, &ref, &amt, &qty); err != nil {
			return t, err
		}
		if dataprovider.RecordKind(kind) == RecordAllocation {
			t.Allocated[ref] += domain.Money(amt)
			continue
		}
		t.Cash += cashEffect(dataprovider.RecordKind(kind), domain.Money(amt))
		if cust != "" {
			t.CustomerDelta[cust] += domain.Money(amt)
		}
		if prod != "" {
			t.ProductSold[prod] += qty
		}
	}
	return t, rows.Err()
}

func (l PGLedger) Range(ctx context.Context, c domain.CompanyID, from, to time.Time) ([]Entry, error) {
	rows, err := l.DB.Query(ctx, `
		SELECT draft_id, kind, customer_id, product_id, amount, vat, quantity, ref, at
		FROM fake_ledger WHERE company_id = $1 AND at >= $2 AND at < $3`, string(c), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var kind string
		var amt, vat int64
		if err := rows.Scan(&e.DraftID, &kind, &e.CustomerID, &e.ProductID, &amt, &vat, &e.Quantity, &e.Ref, &e.At); err != nil {
			return nil, err
		}
		e.CompanyID, e.Kind, e.Amount, e.VAT = c, dataprovider.RecordKind(kind), domain.Money(amt), domain.Money(vat)
		out = append(out, e)
	}
	return out, rows.Err()
}
