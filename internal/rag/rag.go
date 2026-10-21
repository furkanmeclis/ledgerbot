// Package rag stores and searches per-company knowledge with pgvector:
// company notes, committed records and documents. Every query is filtered by
// company id first, so tenants never see each other's chunks.
//
// Answers are the matching chunks themselves, returned verbatim; no model
// writes free text from them, so nothing is invented.
package rag

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Chunk is one searchable piece of text.
type Chunk struct {
	ID      int64   `json:"id"`
	Source  string  `json:"source"` // note, record, document
	Ref     string  `json:"ref,omitempty"`
	Content string  `json:"content"`
	Score   float64 `json:"score"` // cosine similarity, 1 = identical
}

// Index stores and searches chunks.
type Index interface {
	Add(ctx context.Context, c domain.CompanyID, source, ref, content string) error
	Search(ctx context.Context, c domain.CompanyID, query string, k int) ([]Chunk, error)
}

// PG is the pgvector index. The HNSW index is an expression index on
// embedding::vector(dims), created on first use for the configured dimension.
type PG struct {
	DB       *pgxpool.Pool
	Embedder ai.Embedder

	once sync.Once
	err  error
}

func (p *PG) ensure(ctx context.Context) error {
	p.once.Do(func() {
		d := p.Embedder.Dimensions()
		if d <= 0 || d > 16000 {
			p.err = fmt.Errorf("rag: invalid embedding dimensions %d", d)
			return
		}
		ds := strconv.Itoa(d)
		_, p.err = p.DB.Exec(ctx, `CREATE INDEX IF NOT EXISTS rag_chunks_hnsw_`+ds+` ON rag_chunks
			USING hnsw ((embedding::vector(`+ds+`)) vector_cosine_ops) WHERE vector_dims(embedding) = `+ds)
	})
	return p.err
}

// Add embeds and stores one chunk. Long texts are split on paragraph boundaries.
func (p *PG) Add(ctx context.Context, c domain.CompanyID, source, ref, content string) error {
	if err := p.ensure(ctx); err != nil {
		return err
	}
	parts := Split(content, 800)
	if len(parts) == 0 {
		return nil
	}
	vecs, _, err := p.Embedder.Embed(ctx, parts)
	if err != nil {
		return err
	}
	b := &pgxBatch{}
	for i, part := range parts {
		b.add(`INSERT INTO rag_chunks (company_id, source, ref, content, embedding) VALUES ($1,$2,$3,$4,$5::vector)`,
			string(c), source, ref, part, Literal(vecs[i]))
	}
	return b.run(ctx, p.DB)
}

// Search returns the k most similar chunks of a company.
func (p *PG) Search(ctx context.Context, c domain.CompanyID, query string, k int) ([]Chunk, error) {
	if err := p.ensure(ctx); err != nil {
		return nil, err
	}
	if k <= 0 || k > 20 {
		k = 5
	}
	vecs, _, err := p.Embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	ds := strconv.Itoa(p.Embedder.Dimensions())
	rows, err := p.DB.Query(ctx, `
		SELECT id, source, ref, content, 1 - (embedding::vector(`+ds+`) <=> $2::vector(`+ds+`)) AS score
		FROM rag_chunks
		WHERE company_id = $1 AND vector_dims(embedding) = `+ds+`
		ORDER BY embedding::vector(`+ds+`) <=> $2::vector(`+ds+`)
		LIMIT $3`, string(c), Literal(vecs[0]), k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chunk
	for rows.Next() {
		var ch Chunk
		if err := rows.Scan(&ch.ID, &ch.Source, &ch.Ref, &ch.Content, &ch.Score); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// Literal formats a vector as a pgvector text literal ("[0.1,0.2]").
func Literal(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 10)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', 7, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Split cuts text into chunks of at most max runes, preferring paragraph and
// sentence boundaries.
func Split(text string, maxRunes int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			out = append(out, s)
		}
		cur.Reset()
	}
	for _, para := range strings.Split(text, "\n\n") {
		for _, sent := range strings.SplitAfter(para, ". ") {
			r := []rune(sent)
			for len(r) > maxRunes { // a single huge sentence
				flush()
				out = append(out, string(r[:maxRunes]))
				r = r[maxRunes:]
			}
			if cur.Len() > 0 && len([]rune(cur.String()))+len(r) > maxRunes {
				flush()
			}
			cur.WriteString(string(r))
		}
		cur.WriteString("\n")
	}
	flush()
	return out
}

// Mem is an in-memory Index for tests (exact cosine over all chunks).
type Mem struct {
	Embedder ai.Embedder
	mu       sync.Mutex
	chunks   map[domain.CompanyID][]memChunk
}

type memChunk struct {
	Chunk
	vec []float32
}

func (m *Mem) Add(ctx context.Context, c domain.CompanyID, source, ref, content string) error {
	parts := Split(content, 800)
	vecs, _, err := m.Embedder.Embed(ctx, parts)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chunks == nil {
		m.chunks = map[domain.CompanyID][]memChunk{}
	}
	for i, p := range parts {
		id := int64(len(m.chunks[c]) + 1)
		m.chunks[c] = append(m.chunks[c], memChunk{Chunk{ID: id, Source: source, Ref: ref, Content: p}, vecs[i]})
	}
	return nil
}

func (m *Mem) Search(ctx context.Context, c domain.CompanyID, q string, k int) ([]Chunk, error) {
	vecs, _, err := m.Embedder.Embed(ctx, []string{q})
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Chunk
	for _, ch := range m.chunks[c] {
		s := cosine(vecs[0], ch.vec)
		cc := ch.Chunk
		cc.Score = s
		out = append(out, cc)
	}
	for i := 1; i < len(out); i++ { // small n: insertion sort by score desc
		for j := i; j > 0 && out[j].Score > out[j-1].Score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	return out, nil
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		if i >= len(b) {
			break
		}
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}
