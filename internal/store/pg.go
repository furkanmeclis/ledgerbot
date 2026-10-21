package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// PG is the PostgreSQL Store.
type PG struct{ DB *pgxpool.Pool }

var _ Store = (*PG)(nil)

const draftCols = `id, company_id, actor_id, capability, status, payload, summary, external_id, channel, sender, created_at, updated_at`

func scanDraft(row pgx.Row) (domain.Draft, error) {
	var d domain.Draft
	var company, actor, status, channel string
	err := row.Scan(&d.ID, &company, &actor, &d.Capability, &status, &d.Payload, &d.Summary, &d.ExternalID, &channel, &d.Sender, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	d.CompanyID, d.ActorID, d.Status, d.Channel = domain.CompanyID(company), domain.UserID(actor), domain.DraftStatus(status), domain.ChannelKind(channel)
	return d, err
}

func (p *PG) CreateDraft(ctx context.Context, d *domain.Draft) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	d.UpdatedAt = d.CreatedAt
	_, err := p.DB.Exec(ctx, `INSERT INTO drafts (`+draftCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		d.ID, string(d.CompanyID), string(d.ActorID), d.Capability, string(d.Status), d.Payload, d.Summary, d.ExternalID,
		string(d.Channel), d.Sender, d.CreatedAt, d.UpdatedAt)
	return err
}

func (p *PG) GetDraft(ctx context.Context, c domain.CompanyID, id string) (domain.Draft, error) {
	return scanDraft(p.DB.QueryRow(ctx, `SELECT `+draftCols+` FROM drafts WHERE company_id = $1 AND id = $2`, string(c), id))
}

func (p *PG) TransitionDraft(ctx context.Context, c domain.CompanyID, id string, from, to domain.DraftStatus, ext string) (domain.Draft, error) {
	d, err := scanDraft(p.DB.QueryRow(ctx, `
		UPDATE drafts SET status = $4, external_id = CASE WHEN $5 = '' THEN external_id ELSE $5 END, updated_at = now()
		WHERE company_id = $1 AND id = $2 AND status = $3
		RETURNING `+draftCols, string(c), id, string(from), string(to), ext))
	if errors.Is(err, ErrNotFound) {
		if cur, gerr := p.GetDraft(ctx, c, id); gerr == nil {
			return cur, ErrConflict
		}
	}
	return d, err
}

func (p *PG) ListDrafts(ctx context.Context, c domain.CompanyID, f DraftFilter) ([]domain.Draft, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	q := `SELECT ` + draftCols + ` FROM drafts WHERE company_id = $1`
	args := []any{string(c)}
	if f.Status != "" {
		args = append(args, string(f.Status))
		q += ` AND status = $` + itoa(len(args))
	}
	if f.ActorID != "" {
		args = append(args, string(f.ActorID))
		q += ` AND actor_id = $` + itoa(len(args))
	}
	if !f.Before.IsZero() {
		args = append(args, f.Before)
		q += ` AND created_at < $` + itoa(len(args))
	}
	args = append(args, f.Limit)
	q += ` ORDER BY created_at DESC LIMIT $` + itoa(len(args))
	rows, err := p.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Draft
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *PG) LastCommitted(ctx context.Context, c domain.CompanyID, actor domain.UserID, caps []string) (domain.Draft, error) {
	return scanDraft(p.DB.QueryRow(ctx, `
		SELECT `+draftCols+` FROM drafts
		WHERE company_id = $1 AND actor_id = $2 AND status = 'committed' AND capability = ANY($3)
		ORDER BY created_at DESC LIMIT 1`, string(c), string(actor), caps))
}

func (p *PG) LatestPending(ctx context.Context, c domain.CompanyID, ch domain.ChannelKind, sender string, maxAge time.Duration) (domain.Draft, error) {
	return scanDraft(p.DB.QueryRow(ctx, `
		SELECT `+draftCols+` FROM drafts
		WHERE company_id = $1 AND channel = $2 AND sender = $3 AND status = 'pending' AND created_at > $4
		ORDER BY created_at DESC LIMIT 1`, string(c), string(ch), sender, time.Now().Add(-maxAge)))
}

func (p *PG) ClaimFingerprint(ctx context.Context, c domain.CompanyID, fp, draftID string) (string, bool, error) {
	tag, err := p.DB.Exec(ctx, `INSERT INTO fingerprints (company_id, fp, draft_id) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, string(c), fp, draftID)
	if err != nil {
		return "", false, err
	}
	if tag.RowsAffected() == 1 {
		return "", false, nil
	}
	var prev string
	err = p.DB.QueryRow(ctx, `SELECT draft_id FROM fingerprints WHERE company_id = $1 AND fp = $2`, string(c), fp).Scan(&prev)
	return prev, true, err
}

func (p *PG) RecordUsage(ctx context.Context, u UsageRecord) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO ai_usage (company_id, purpose, provider, model, input_tokens, output_tokens, audio_seconds, cost_micro_usd, at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		string(u.CompanyID), u.Purpose, u.Provider, u.Model, u.InputTokens, u.OutputTokens, u.AudioSeconds, u.CostMicroUSD, u.At)
	return err
}

func (p *PG) CallsSince(ctx context.Context, c domain.CompanyID, since time.Time) (int64, error) {
	var n int64
	err := p.DB.QueryRow(ctx, `SELECT count(*) FROM ai_usage WHERE company_id = $1 AND at >= $2`, string(c), since).Scan(&n)
	return n, err
}

func (p *PG) UsageReport(ctx context.Context, c domain.CompanyID, from, to time.Time) ([]UsageSummary, error) {
	rows, err := p.DB.Query(ctx, `
		SELECT purpose, provider, model, count(*), sum(input_tokens), sum(output_tokens), sum(audio_seconds), sum(cost_micro_usd)
		FROM ai_usage WHERE company_id = $1 AND at >= $2 AND at < $3
		GROUP BY purpose, provider, model ORDER BY purpose, provider, model`, string(c), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSummary
	for rows.Next() {
		var s UsageSummary
		var cost int64
		if err := rows.Scan(&s.Purpose, &s.Provider, &s.Model, &s.Calls, &s.InputTokens, &s.OutputTokens, &s.AudioSeconds, &cost); err != nil {
			return nil, err
		}
		s.CostUSD = float64(cost) / 1e6
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PG) Audit(ctx context.Context, e AuditEntry) error {
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	_, err := p.DB.Exec(ctx, `INSERT INTO audit_log (company_id, actor_id, channel, action, draft_id, detail, at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		string(e.CompanyID), string(e.ActorID), string(e.Channel), e.Action, e.DraftID, nullJSON(e.Detail), e.At)
	return err
}

func (p *PG) ListAudit(ctx context.Context, c domain.CompanyID, beforeID int64, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = 1<<63 - 1
	}
	rows, err := p.DB.Query(ctx, `SELECT id, company_id, actor_id, channel, action, draft_id, detail, at FROM audit_log
		WHERE company_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, string(c), beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var company, actor, ch string
		if err := rows.Scan(&e.ID, &company, &actor, &ch, &e.Action, &e.DraftID, &e.Detail, &e.At); err != nil {
			return nil, err
		}
		e.CompanyID, e.ActorID, e.Channel = domain.CompanyID(company), domain.UserID(actor), domain.ChannelKind(ch)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (p *PG) CreateDeletion(ctx context.Context, r DeletionRequest) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO deletion_requests (id, company_id, channel, subject, status, code, requested_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		r.ID, string(r.CompanyID), string(r.Channel), r.Subject, r.Status, r.Code, r.RequestedAt)
	return err
}

func (p *PG) GetDeletion(ctx context.Context, id string) (DeletionRequest, error) {
	var r DeletionRequest
	var company, ch string
	err := p.DB.QueryRow(ctx, `SELECT id, company_id, channel, subject, status, code, requested_at, completed_at, removed FROM deletion_requests WHERE id = $1`, id).
		Scan(&r.ID, &company, &ch, &r.Subject, &r.Status, &r.Code, &r.RequestedAt, &r.CompletedAt, &r.Removed)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrNotFound
	}
	r.CompanyID, r.Channel = domain.CompanyID(company), domain.ChannelKind(ch)
	return r, err
}

// EraseSubject removes a subject's drafts and redacts audit rows mentioning it.
func (p *PG) EraseSubject(ctx context.Context, r DeletionRequest) (int64, error) {
	tx, err := p.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var total int64
	tag, err := tx.Exec(ctx, `DELETE FROM drafts WHERE sender = $1 AND ($2 = '' OR company_id = $2)`, r.Subject, string(r.CompanyID))
	if err != nil {
		return 0, err
	}
	total += tag.RowsAffected()
	tag, err = tx.Exec(ctx, `UPDATE audit_log SET detail = '{"redacted":true}'::jsonb
		WHERE ($2 = '' OR company_id = $2) AND detail::text LIKE '%' || $1 || '%'`, escapeLike(r.Subject), string(r.CompanyID))
	if err != nil {
		return 0, err
	}
	total += tag.RowsAffected()
	return total, tx.Commit(ctx)
}

func (p *PG) CompleteDeletion(ctx context.Context, id string, removed int64) error {
	_, err := p.DB.Exec(ctx, `UPDATE deletion_requests SET status = 'completed', completed_at = now(), removed = $2 WHERE id = $1`, id, removed)
	return err
}

func (p *PG) UpsertConnection(ctx context.Context, c *Connection) error {
	c.UpdatedAt = time.Now().UTC()
	_, err := p.DB.Exec(ctx, `INSERT INTO channel_connections (id, company_id, kind, mode, name, enabled, config, status, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (id) DO UPDATE SET mode = EXCLUDED.mode, name = EXCLUDED.name, enabled = EXCLUDED.enabled,
			config = EXCLUDED.config, status = EXCLUDED.status, updated_at = EXCLUDED.updated_at`,
		c.ID, string(c.CompanyID), string(c.Kind), c.Mode, c.Name, c.Enabled, c.Config, c.Status, c.UpdatedAt)
	return err
}

const connCols = `id, company_id, kind, mode, name, enabled, config, status, updated_at`

func scanConn(row pgx.Row) (Connection, error) {
	var c Connection
	var company, kind string
	err := row.Scan(&c.ID, &company, &kind, &c.Mode, &c.Name, &c.Enabled, &c.Config, &c.Status, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	c.CompanyID, c.Kind = domain.CompanyID(company), domain.ChannelKind(kind)
	return c, err
}

func (p *PG) GetConnection(ctx context.Context, id string) (Connection, error) {
	return scanConn(p.DB.QueryRow(ctx, `SELECT `+connCols+` FROM channel_connections WHERE id = $1`, id))
}

func (p *PG) ListConnections(ctx context.Context, c domain.CompanyID) ([]Connection, error) {
	rows, err := p.DB.Query(ctx, `SELECT `+connCols+` FROM channel_connections WHERE $1 = '' OR company_id = $1 ORDER BY id`, string(c))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		cn, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cn)
	}
	return out, rows.Err()
}

func (p *PG) DeleteConnection(ctx context.Context, c domain.CompanyID, id string) error {
	tag, err := p.DB.Exec(ctx, `DELETE FROM channel_connections WHERE company_id = $1 AND id = $2`, string(c), id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *PG) SetConnectionStatus(ctx context.Context, id, status string) error {
	_, err := p.DB.Exec(ctx, `UPDATE channel_connections SET status = $2, updated_at = now() WHERE id = $1`, id, status)
	return err
}

func (p *PG) GetIdem(ctx context.Context, c domain.CompanyID, key string) (IdemRecord, bool, error) {
	var r IdemRecord
	err := p.DB.QueryRow(ctx, `SELECT status, body, hash FROM idempotency_keys WHERE company_id = $1 AND key = $2 AND expires_at > now()`,
		string(c), key).Scan(&r.Status, &r.Body, &r.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

func (p *PG) PutIdem(ctx context.Context, c domain.CompanyID, key string, r IdemRecord, ttl time.Duration) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO idempotency_keys (company_id, key, status, body, hash, expires_at) VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (company_id, key) DO NOTHING`, string(c), key, r.Status, r.Body, r.Hash, time.Now().Add(ttl))
	return err
}

func (p *PG) ThrottleOnce(ctx context.Context, c domain.CompanyID, key string, ttl time.Duration) (bool, error) {
	tag, err := p.DB.Exec(ctx, `INSERT INTO throttles (company_id, key, until) VALUES ($1,$2,$3)
		ON CONFLICT (company_id, key) DO UPDATE SET until = EXCLUDED.until WHERE throttles.until < now()`,
		string(c), key, time.Now().Add(ttl))
	return tag.RowsAffected() == 1, err
}

func (p *PG) PutBlob(ctx context.Context, id string, data []byte, ttl time.Duration) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO blobs (id, data, expires_at) VALUES ($1,$2,$3) ON CONFLICT (id) DO NOTHING`, id, data, time.Now().Add(ttl))
	return err
}

func (p *PG) GetBlob(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := p.DB.QueryRow(ctx, `SELECT data FROM blobs WHERE id = $1`, id).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (p *PG) DeleteBlob(ctx context.Context, id string) error {
	_, err := p.DB.Exec(ctx, `DELETE FROM blobs WHERE id = $1`, id)
	return err
}

func (p *PG) Cleanup(ctx context.Context) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM blobs WHERE expires_at < now()`,
		`DELETE FROM idempotency_keys WHERE expires_at < now()`,
		`DELETE FROM throttles WHERE until < now()`,
		`DELETE FROM dev_outbox WHERE at < now() - interval '7 days'`,
	} {
		tag, err := p.DB.Exec(ctx, q)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// AppendDev stores a dev-channel reply and notifies listeners (dashboard stream).
func (p *PG) AppendDev(ctx context.Context, out domain.OutboundMessage) error {
	_, err := p.DB.Exec(ctx, `WITH ins AS (
			INSERT INTO dev_outbox (company_id, recipient, text) VALUES ($1,$2,$3) RETURNING id
		) SELECT pg_notify('ledgerbot_dev', $1 || '|' || $2) FROM ins`, string(out.CompanyID), out.To, out.Text)
	return err
}

// ListDev returns the latest dev replies for a recipient, oldest first.
func (p *PG) ListDev(ctx context.Context, c domain.CompanyID, to string, limit int) ([]domain.OutboundMessage, error) {
	rows, err := p.DB.Query(ctx, `SELECT id, text, at FROM (
			SELECT id, text, at FROM dev_outbox WHERE company_id = $1 AND recipient = $2 ORDER BY id DESC LIMIT $3
		) t ORDER BY id`, string(c), to, max(limit, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.OutboundMessage
	for rows.Next() {
		m := domain.OutboundMessage{CompanyID: c, Channel: domain.ChannelDev, To: to}
		if err := rows.Scan(&m.ID, &m.Text, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (p *PG) SetActiveCompany(ctx context.Context, ch domain.ChannelKind, addr string, c domain.CompanyID) error {
	_, err := p.DB.Exec(ctx, `INSERT INTO active_companies (channel, address, company_id) VALUES ($1,$2,$3)
		ON CONFLICT (channel, address) DO UPDATE SET company_id = EXCLUDED.company_id, updated_at = now()`, string(ch), addr, string(c))
	return err
}

func (p *PG) ActiveCompany(ctx context.Context, ch domain.ChannelKind, addr string) (domain.CompanyID, error) {
	var c string
	err := p.DB.QueryRow(ctx, `SELECT company_id FROM active_companies WHERE channel = $1 AND address = $2`, string(ch), addr).Scan(&c)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return domain.CompanyID(c), err
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return digits[i : i+1]
	}
	return itoa(i/10) + digits[i%10:i%10+1]
}
