package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/rag"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

// Knowledge is the optional RAG backend. Remember queues text for indexing
// (embedding happens off the hot path); Search runs synchronously.
type Knowledge struct {
	Index    rag.Index
	Remember func(ctx context.Context, c domain.CompanyID, source, ref, text string) error
	// MinScore filters weak matches (cosine similarity).
	MinScore float64
}

// writeCaps are the capabilities "her zamanki gibi" can repeat.
var writeCaps = []string{"record_collection", "record_payment", "record_expense", "create_order"}

func memoryCapabilities() []Capability {
	return []Capability{
		{Name: "repeat_last", Description: "Repeat the user's last record (same counterparty, method and category), optionally with a new amount", Args: []string{"amount", "currency"}, Write: true, Run: runRepeatLast},
		{Name: "add_note", Description: "Save a company note to the assistant's memory", Args: []string{"text"}, Run: runAddNote},
		{Name: "search_knowledge", Description: "Search company notes, past records and documents", Args: []string{"query"}, Run: runSearchKnowledge},
	}
}

// runRepeatLast implements "her zamanki gibi": the user's latest committed
// record is cloned with today's date and, when given, a new amount.
func runRepeatLast(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	last, err := e.Store.LastCommitted(ctx, c.Actor.CompanyID, c.Actor.UserID, writeCaps)
	if errors.Is(err, store.ErrNotFound) {
		return Outcome{Text: T(c.Lang, "repeat_none")}, nil
	}
	if err != nil {
		return Outcome{}, err
	}
	var rec dataprovider.Record
	if err := json.Unmarshal(last.Payload, &rec); err != nil {
		return Outcome{}, err
	}
	if !c.Snap.Allowed(c.Actor.Role, last.Capability, c.Msg.Channel) {
		return Outcome{Text: T(c.Lang, "denied", last.Capability)}, nil
	}
	rec.Date, rec.DraftID, rec.Note = e.now(), "", c.Msg.Text
	summary := strings.TrimPrefix(last.Summary, T(c.Lang, "repeat_prefix"))
	if c.Args["amount"] != "" {
		amt, note, msg, err := amountTRY(ctx, e, c)
		if err != nil || msg != "" {
			return Outcome{Text: msg}, err
		}
		if len(rec.Lines) > 0 && rec.Kind == dataprovider.RecordOrder {
			return Outcome{Text: T(c.Lang, "repeat_order_amount")}, nil
		}
		old := rec.Amount
		rec.Amount = amt
		if old != 0 && rec.VAT != 0 {
			rec.VAT = domain.Money(int64(rec.VAT) * int64(amt) / int64(old))
		}
		rec.Lines = nil // a new total no longer matches the old item lines
		summary = strings.Replace(summary, old.String(), amt.String(), 1) + note
	}
	if rec.Kind == dataprovider.RecordCollection {
		rec.Allocations = nil
		summary, _, _ = strings.Cut(summary, "\n") // drop the old allocation lines
		note, err := matchCollection(ctx, e, c.Actor.CompanyID, &rec, c.Lang)
		if err != nil {
			return Outcome{}, err
		}
		summary += note
	}
	return Outcome{Record: &rec, Summary: T(c.Lang, "repeat_prefix") + summary, Capability: last.Capability}, nil
}

func runAddNote(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	text := strings.TrimSpace(c.Args["text"])
	if text == "" || e.Knowledge == nil || e.Knowledge.Remember == nil {
		return Outcome{Text: T(c.Lang, "knowledge_off")}, nil
	}
	note := fmt.Sprintf("%s (%s, %s)", text, c.Actor.Name, e.now().Format("02.01.2006"))
	if err := e.Knowledge.Remember(ctx, c.Actor.CompanyID, "note", string(c.Actor.UserID), note); err != nil {
		return Outcome{}, err
	}
	return Outcome{Text: T(c.Lang, "note_saved")}, nil
}

func runSearchKnowledge(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
	q := strings.TrimSpace(c.Args["query"])
	if e.Knowledge == nil || e.Knowledge.Index == nil {
		return Outcome{Text: T(c.Lang, "knowledge_off")}, nil
	}
	hits, err := e.Knowledge.Index.Search(ctx, c.Actor.CompanyID, q, 5)
	if err != nil {
		return Outcome{}, err
	}
	var b strings.Builder
	n := 0
	for _, h := range hits {
		if h.Score < e.Knowledge.MinScore {
			continue
		}
		n++
		fmt.Fprintf(&b, "• [%s] %s\n", T(c.Lang, "src_"+h.Source), h.Content)
		if n == 3 {
			break
		}
	}
	if n == 0 {
		return Outcome{Text: T(c.Lang, "knowledge_none", q)}, nil
	}
	return Outcome{Text: T(c.Lang, "knowledge_hits", strings.TrimSpace(b.String()))}, nil
}

// rememberRecord indexes a committed draft so "kayıtlarda X ara" and future
// suggestions can find it. Failures never affect the commit.
func (e *Engine) rememberRecord(ctx context.Context, d domain.Draft) {
	if e.Knowledge == nil || e.Knowledge.Remember == nil {
		return
	}
	text := d.CreatedAt.Format("02.01.2006") + " " + d.Summary
	if err := e.Knowledge.Remember(ctx, d.CompanyID, "record", d.ID, text); err != nil {
		e.Log.Warn("remember record failed", "draft", d.ID, "err", err)
	}
}
