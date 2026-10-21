package rag

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxBatch struct{ b pgx.Batch }

func (p *pgxBatch) add(sql string, args ...any) { p.b.Queue(sql, args...) }

// run sends all statements in one round trip.
func (p *pgxBatch) run(ctx context.Context, db *pgxpool.Pool) error {
	return db.SendBatch(ctx, &p.b).Close()
}

func sqrt(x float64) float64 { return math.Sqrt(x) }
