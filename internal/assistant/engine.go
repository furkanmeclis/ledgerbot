// Package assistant is ledgerbot's core: it turns an inbound message into
// intents, checks permissions, runs capabilities, applies the save mode and
// produces verbatim replies.
//
// Principles (from the Contiva design notes):
//   - The LLM only understands intent; capabilities compute every number.
//   - Capability output is sent to the user verbatim.
//   - Write capabilities obey the save mode: reply_only, ask_approval, auto_save.
//   - Yes/no approvals are parsed without any model.
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/ai"
	"github.com/furkanmeclis/ledgerbot/internal/bank"
	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/fx"
	"github.com/furkanmeclis/ledgerbot/internal/nlu"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/telemetry"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Tenants returns the settings + permission snapshot for a company.
type Tenants interface {
	Get(ctx context.Context, c domain.CompanyID) (*tenant.Snapshot, error)
}

// Engine wires the assistant's dependencies. It is safe for concurrent use.
type Engine struct {
	Tenants Tenants
	Data    dataprovider.Provider
	AI      *ai.Registry
	Store   store.Store
	Events  events.Publisher
	FX      *fx.Converter
	Caps    *Registry
	// Knowledge enables memory and RAG capabilities (optional).
	Knowledge *Knowledge
	Log       *slog.Logger
	Now       func() time.Time
	// ApprovalTTL bounds how long a pending draft can be approved by "evet".
	ApprovalTTL time.Duration
}

// Reply is what goes back to the sender.
type Reply struct {
	Texts    []string       `json:"texts"`
	Language string         `json:"language"`
	Drafts   []domain.Draft `json:"drafts,omitempty"`
	Intents  []nlu.Intent   `json:"intents,omitempty"`
}

func (r *Reply) add(s string) {
	if s != "" {
		r.Texts = append(r.Texts, s)
	}
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Handle processes one inbound message end to end.
func (e *Engine) Handle(ctx context.Context, msg domain.InboundMessage) (Reply, error) {
	ctx, span := telemetry.Tracer().Start(ctx, "assistant.handle", trace.WithAttributes(
		attribute.String("ledgerbot.company", string(msg.CompanyID)),
		attribute.String("ledgerbot.channel", string(msg.Channel)),
		attribute.String("ledgerbot.kind", string(msg.Kind)),
	))
	defer span.End()
	snap, err := e.Tenants.Get(ctx, msg.CompanyID)
	if err != nil {
		return Reply{}, err
	}
	lang := nlu.DetectLanguage(msg.Text)
	actor, ok := snap.ResolveActor(msg.Channel, msg.Sender)
	if !ok {
		e.audit(ctx, store.AuditEntry{CompanyID: msg.CompanyID, Channel: msg.Channel, Action: "sender.unknown", Detail: mustJSON(map[string]string{"sender": msg.Sender})})
		return Reply{Texts: []string{T(lang, "unknown_sender")}, Language: lang}, nil
	}
	e.audit(ctx, store.AuditEntry{CompanyID: msg.CompanyID, ActorID: actor.UserID, Channel: msg.Channel, Action: "message.received",
		Detail: mustJSON(map[string]any{"kind": msg.Kind, "id": msg.ID, "sender": msg.Sender})})

	call := &Call{Snap: snap, Actor: actor, Msg: msg, Lang: lang}
	var reply Reply

	switch msg.Kind {
	case domain.KindAudio:
		if !e.quotaOK(ctx, snap) {
			return Reply{Texts: []string{T(lang, "quota")}, Language: lang}, nil
		}
		text, err := e.transcribe(ctx, snap, msg)
		if err != nil {
			return Reply{}, err
		}
		if strings.TrimSpace(text) == "" {
			return Reply{Texts: []string{T(lang, "empty_audio")}, Language: lang}, nil
		}
		call.Lang = nlu.DetectLanguage(text)
		reply.add(T(call.Lang, "heard", text))
		msg.Text = text
		call.Msg.Text = text
	case domain.KindImage, domain.KindDocument:
		call.Args = map[string]string{}
		reply.Language = call.Lang
		capName := "record_expense"
		if m := msg.Media; m != nil && !strings.HasPrefix(m.MIME, "image/") && bank.Looks(m.MIME, m.Filename, msg.Text) {
			capName = "import_bank_statement"
		}
		args := map[string]string{}
		if msg.Media != nil {
			args["mime"] = msg.Media.MIME
		}
		reply.Intents = []nlu.Intent{{Capability: capName, Source: "document", Args: args}}
		e.runCapability(ctx, call, capName, &reply)
		return reply, nil
	}

	// Approval replies never reach a model.
	if a := nlu.ParseApproval(msg.Text); a != nlu.NoApproval {
		d, err := e.Store.LatestPending(ctx, msg.CompanyID, msg.Channel, msg.Sender, e.approvalTTL())
		if errors.Is(err, store.ErrNotFound) {
			reply.add(T(call.Lang, "nothing_pending"))
			reply.Language = call.Lang
			return reply, nil
		}
		if err != nil {
			return Reply{}, err
		}
		var out domain.Draft
		var text string
		if a == nlu.Yes {
			out, text, err = e.Approve(ctx, snap, actor, d.ID, call.Lang)
		} else {
			out, text, err = e.Reject(ctx, snap, actor, d.ID, call.Lang)
		}
		if err != nil && !errors.Is(err, store.ErrConflict) {
			return Reply{}, err
		}
		reply.add(text)
		reply.Drafts = append(reply.Drafts, out)
		reply.Language = call.Lang
		return reply, nil
	}

	parser := &nlu.Parser{Capabilities: e.docs(snap), Instruction: snap.SystemPrompt(actor.Role)}
	if len(snap.Settings.Plugins) > 0 {
		parser.Before = func(text string) (nlu.Intent, bool) { return matchPlugins(snap, text) }
	}
	allowLLM := e.quotaOK(ctx, snap)
	if allowLLM {
		if m, err := e.AI.LLM(ai.PurposeAgent, snap.Settings.Providers.Agent); err == nil {
			parser.LLM = m
		}
	}
	res, err := parser.Parse(ctx, msg.Text, allowLLM)
	if err != nil {
		e.Log.Warn("nlu failed", "company", msg.CompanyID, "err", err)
		res = nlu.Result{Intents: []nlu.Intent{{Capability: "unknown"}}, Language: call.Lang}
	}
	if res.Usage != nil {
		e.recordUsage(ctx, snap, ai.PurposeAgent, snap.Settings.Providers.Agent, *res.Usage)
	}
	if msg.Kind != domain.KindAudio {
		call.Lang = res.Language
	}
	reply.Language = call.Lang
	reply.Intents = res.Intents
	e.audit(ctx, store.AuditEntry{CompanyID: msg.CompanyID, ActorID: actor.UserID, Channel: msg.Channel, Action: "intent.parsed", Detail: mustJSON(res.Intents)})

	for _, in := range res.Intents {
		c := *call
		c.Args = in.Args
		e.runCapability(ctx, &c, in.Capability, &reply)
	}
	_ = e.publish(ctx, msg.CompanyID, events.MessageHandled, map[string]any{
		"message_id": msg.ID, "channel": msg.Channel, "actor_id": actor.UserID, "intents": res.Intents,
	})
	return reply, nil
}

func (e *Engine) runCapability(ctx context.Context, call *Call, name string, reply *Reply) {
	ctx, span := telemetry.Tracer().Start(ctx, "capability."+name)
	defer span.End()
	if name == "unknown" || name == "" {
		reply.add(T(call.Lang, "unknown_intent"))
		return
	}
	capb, ok := e.capability(call.Snap, name)
	if !ok {
		reply.add(T(call.Lang, "unknown_intent"))
		return
	}
	if !call.Snap.Allowed(call.Actor.Role, name, call.Msg.Channel) {
		e.audit(ctx, store.AuditEntry{CompanyID: call.Actor.CompanyID, ActorID: call.Actor.UserID, Channel: call.Msg.Channel, Action: "permission.denied", Detail: mustJSON(map[string]string{"capability": name})})
		reply.add(T(call.Lang, "denied", name))
		return
	}
	if capb.Write && call.Snap.Degraded {
		reply.add(T(call.Lang, "degraded"))
	}
	out, err := capb.Run(ctx, e, call)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		e.Log.Error("capability failed", "capability", name, "company", call.Actor.CompanyID, "err", err)
		reply.add(T(call.Lang, "error"))
		return
	}
	if out.Record == nil {
		reply.add(out.Text)
		return
	}
	d, text, err := e.saveDraft(ctx, call, name, out)
	if err != nil {
		e.Log.Error("save draft failed", "capability", name, "err", err)
		reply.add(T(call.Lang, "error"))
		return
	}
	reply.add(text)
	if d.ID != "" {
		reply.Drafts = append(reply.Drafts, d)
	}
}

// saveDraft applies duplicate detection and the save mode.
func (e *Engine) saveDraft(ctx context.Context, call *Call, capability string, out Outcome) (domain.Draft, string, error) {
	if out.Capability != "" {
		capability = out.Capability
	}
	id := events.NewID("drf")
	if out.Fingerprint != "" {
		prev, dup, err := e.Store.ClaimFingerprint(ctx, call.Actor.CompanyID, out.Fingerprint, id)
		if err != nil {
			return domain.Draft{}, "", err
		}
		if dup {
			_ = e.publish(ctx, call.Actor.CompanyID, events.DraftDuplicate, map[string]string{"previous_draft_id": prev, "capability": capability})
			e.audit(ctx, store.AuditEntry{CompanyID: call.Actor.CompanyID, ActorID: call.Actor.UserID, Channel: call.Msg.Channel, Action: "draft.duplicate", DraftID: prev})
			return domain.Draft{}, T(call.Lang, "duplicate"), nil
		}
	}
	mode := call.Snap.EffectiveSaveMode(call.Actor.Role)
	rec := *out.Record
	if warn := e.anomalies(ctx, call, capability, rec); len(warn) > 0 {
		out.Text = strings.TrimSpace(strings.Join(warn, "\n") + "\n" + out.Text)
		if mode == domain.SaveAuto {
			mode = domain.SaveAskApproval // unusual records always ask first
		}
	}
	rec.DraftID, rec.ActorID = id, call.Actor.UserID
	payload, _ := json.Marshal(rec)
	d := domain.Draft{
		ID: id, CompanyID: call.Actor.CompanyID, ActorID: call.Actor.UserID, Capability: capability,
		Payload: payload, Summary: out.Summary, Channel: call.Msg.Channel, Sender: call.Msg.Sender,
		CreatedAt: e.now().UTC(),
	}
	switch mode {
	case domain.SaveReplyOnly:
		d.Status = domain.DraftPreview
	default:
		d.Status = domain.DraftPending
	}
	if err := e.Store.CreateDraft(ctx, &d); err != nil {
		return d, "", err
	}
	e.audit(ctx, store.AuditEntry{CompanyID: d.CompanyID, ActorID: d.ActorID, Channel: d.Channel, Action: "draft.created", DraftID: d.ID, Detail: mustJSON(map[string]string{"mode": string(mode), "capability": capability})})
	_ = e.publish(ctx, d.CompanyID, events.DraftCreated, d)

	prefix := ""
	if out.Text != "" {
		prefix = out.Text + "\n"
	}
	switch mode {
	case domain.SaveReplyOnly:
		return d, prefix + T(call.Lang, "preview", out.Summary), nil
	case domain.SaveAskApproval:
		return d, prefix + T(call.Lang, "ask", out.Summary), nil
	default:
		cd, text, err := e.commit(ctx, d, call.Lang)
		return cd, prefix + text, err
	}
}

// Approve commits a pending draft (chat "evet" or POST /v1/drafts/{id}/approve).
func (e *Engine) Approve(ctx context.Context, snap *tenant.Snapshot, actor domain.Actor, draftID, lang string) (domain.Draft, string, error) {
	d, err := e.Store.GetDraft(ctx, snap.Settings.CompanyID, draftID)
	if err != nil {
		return d, "", err
	}
	if !snap.Allowed(actor.Role, d.Capability, domain.ChannelAPI) || (snap.OwnScope(actor.Role) && d.ActorID != actor.UserID) {
		return d, T(lang, "denied", d.Capability), store.ErrConflict
	}
	if snap.Degraded {
		return d, T(lang, "degraded"), store.ErrConflict
	}
	if d.Status != domain.DraftPending {
		return d, T(lang, "nothing_pending"), store.ErrConflict
	}
	return e.commit(ctx, d, lang)
}

// Reject cancels a pending draft.
func (e *Engine) Reject(ctx context.Context, snap *tenant.Snapshot, actor domain.Actor, draftID, lang string) (domain.Draft, string, error) {
	d, err := e.Store.GetDraft(ctx, snap.Settings.CompanyID, draftID)
	if err != nil {
		return d, "", err
	}
	if snap.OwnScope(actor.Role) && d.ActorID != actor.UserID {
		return d, T(lang, "denied", d.Capability), store.ErrConflict
	}
	d, err = e.Store.TransitionDraft(ctx, d.CompanyID, d.ID, domain.DraftPending, domain.DraftRejected, "")
	if err != nil {
		return d, T(lang, "nothing_pending"), err
	}
	e.audit(ctx, store.AuditEntry{CompanyID: d.CompanyID, ActorID: actor.UserID, Channel: d.Channel, Action: "draft.rejected", DraftID: d.ID})
	_ = e.publish(ctx, d.CompanyID, events.DraftRejected, d)
	return d, T(lang, "rejected", d.Summary), nil
}

func (e *Engine) commit(ctx context.Context, d domain.Draft, lang string) (domain.Draft, string, error) {
	var rec dataprovider.Record
	if err := json.Unmarshal(d.Payload, &rec); err != nil {
		return d, "", err
	}
	ext, err := e.Data.Commit(ctx, d.CompanyID, rec)
	if err != nil {
		e.Log.Error("commit failed", "draft", d.ID, "err", err)
		e.audit(ctx, store.AuditEntry{CompanyID: d.CompanyID, ActorID: d.ActorID, Channel: d.Channel, Action: "draft.commit_failed", DraftID: d.ID, Detail: mustJSON(map[string]string{"error": err.Error()})})
		return d, T(lang, "commit_failed", d.Summary), nil
	}
	cd, err := e.Store.TransitionDraft(ctx, d.CompanyID, d.ID, domain.DraftPending, domain.DraftCommitted, ext)
	if err != nil {
		return d, T(lang, "nothing_pending"), err
	}
	e.audit(ctx, store.AuditEntry{CompanyID: d.CompanyID, ActorID: d.ActorID, Channel: d.Channel, Action: "draft.committed", DraftID: d.ID, Detail: mustJSON(map[string]string{"external_id": ext})})
	_ = e.publish(ctx, d.CompanyID, events.DraftCommitted, cd)
	e.rememberRecord(ctx, cd)
	return cd, T(lang, "committed", d.Summary), nil
}

func (e *Engine) approvalTTL() time.Duration {
	if e.ApprovalTTL > 0 {
		return e.ApprovalTTL
	}
	return 30 * time.Minute
}

// quotaOK reports whether the company may still call AI providers today.
func (e *Engine) quotaOK(ctx context.Context, snap *tenant.Snapshot) bool {
	limit := snap.Settings.AIDailyLimit
	if limit <= 0 {
		return true
	}
	now := e.now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	n, err := e.Store.CallsSince(ctx, snap.Settings.CompanyID, start)
	if err != nil {
		e.Log.Warn("quota lookup failed", "err", err)
		return true
	}
	return n < int64(limit)
}

func (e *Engine) recordUsage(ctx context.Context, snap *tenant.Snapshot, purpose ai.Purpose, override string, u ai.Usage) {
	var cost int64
	if p, err := e.AI.Profile(purpose, override); err == nil {
		cost = ai.CostMicroUSD(p, u)
	}
	err := e.Store.RecordUsage(ctx, store.UsageRecord{
		CompanyID: snap.Settings.CompanyID, Purpose: string(purpose), Provider: u.Provider, Model: u.Model,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, AudioSeconds: u.AudioSeconds,
		CostMicroUSD: cost, At: e.now().UTC(),
	})
	if err != nil {
		e.Log.Warn("record usage failed", "err", err)
	}
	observeAI(string(purpose), u)
}

func (e *Engine) transcribe(ctx context.Context, snap *tenant.Snapshot, msg domain.InboundMessage) (string, error) {
	if msg.Media == nil {
		return msg.Text, nil
	}
	stt, err := e.AI.STT(snap.Settings.Providers.STT)
	if err != nil {
		return "", err
	}
	tr, err := stt.Transcribe(ctx, *msg.Media, "")
	if err != nil {
		return "", err
	}
	e.recordUsage(ctx, snap, ai.PurposeSTT, snap.Settings.Providers.STT, tr.Usage)
	return tr.Text, nil
}

func (e *Engine) audit(ctx context.Context, a store.AuditEntry) {
	if a.At.IsZero() {
		a.At = e.now().UTC()
	}
	if err := e.Store.Audit(ctx, a); err != nil {
		e.Log.Warn("audit write failed", "err", err)
	}
}

// publish emits an outbound event only when the company subscribes to it.
// Outbound events exist for webhooks; skipping unsubscribed ones keeps the
// queue free of work nobody consumes (the hot path stays one insert lighter).
func (e *Engine) publish(ctx context.Context, c domain.CompanyID, typ string, data any) error {
	if e.Events == nil {
		return nil
	}
	snap, err := e.Tenants.Get(ctx, c)
	if err != nil || !snap.Subscribed(typ) {
		return err
	}
	ev, err := events.New(c, typ, "ledgerbot", data)
	if err != nil {
		return err
	}
	return e.Events.Publish(ctx, ev)
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}
