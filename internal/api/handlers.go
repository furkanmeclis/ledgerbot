package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/furkanmeclis/ledgerbot/internal/channels"
	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/events"
	"github.com/furkanmeclis/ledgerbot/internal/queue"
	"github.com/furkanmeclis/ledgerbot/internal/store"
	"github.com/furkanmeclis/ledgerbot/internal/worker"
)

// mediaIn is media in a JSON request (base64 data).
type mediaIn struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	Data     string `json:"data"`
}

func (m *mediaIn) decode() (*domain.Media, error) {
	if m == nil {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return nil, errors.New("media.data must be base64")
	}
	return &domain.Media{MIME: channels.NormalizeMIME(m.MIME), Filename: m.Filename, Data: data}, nil
}

func kindFor(_ string, m *domain.Media) domain.MessageKind {
	if m == nil {
		return domain.KindText
	}
	return channels.KindFromMIME(m.MIME)
}

// --- capabilities -------------------------------------------------------------

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	snap := snapFrom(r)
	type capOut struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Args        []string `json:"args,omitempty"`
		Write       bool     `json:"write"`
		Roles       []string `json:"roles"`
	}
	var out []capOut
	for _, c := range s.Engine.Caps.List() {
		co := capOut{Name: c.Name, Description: c.Description, Args: c.Args, Write: c.Write, Roles: []string{}}
		for role := range snap.Matrix.Roles {
			if snap.Allowed(role, c.Name, domain.ChannelAPI) {
				co.Roles = append(co.Roles, role)
			}
		}
		out = append(out, co)
	}
	ok(w, http.StatusOK, map[string]any{"capabilities": out, "save_mode": snap.Settings.SaveMode, "degraded": snap.Degraded})
}

// --- messages ---------------------------------------------------------------------

type analyzeReq struct {
	ActorID string   `json:"actor_id"`
	Text    string   `json:"text,omitempty"`
	Media   *mediaIn `json:"media,omitempty"`
	Async   bool     `json:"async,omitempty"`
}

// analyze runs the assistant for a message submitted by the host on behalf of
// a user. Sync by default; async returns a job id and results arrive by webhook.
func (s *Server) analyze(w http.ResponseWriter, r *http.Request) {
	var req analyzeReq
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	snap := snapFrom(r)
	actor, found := snap.ActorByID(domain.UserID(req.ActorID))
	if !found {
		fail(w, http.StatusUnprocessableEntity, "unknown_actor", "actor_id is not in the permission matrix")
		return
	}
	media, err := req.Media.decode()
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.Text == "" && media == nil {
		fail(w, http.StatusBadRequest, "bad_request", "text or media is required")
		return
	}
	// API calls act as the user through a synthetic "api" address.
	msg := domain.InboundMessage{
		ID: events.NewID("api"), CompanyID: snap.Settings.CompanyID, Channel: domain.ChannelAPI,
		Sender: "user:" + string(actor.UserID), Kind: kindFor(req.Text, media), Text: req.Text, Media: media, ReceivedAt: time.Now().UTC(),
	}
	if req.Async {
		if err := worker.Accept(r.Context(), s.Store, s.Queue, msg); err != nil {
			fail(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
			return
		}
		ok(w, http.StatusAccepted, map[string]string{"message_id": msg.ID, "status": "queued"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.SyncTimeout)
	defer cancel()
	reply, err := s.Engine.Handle(ctx, msg)
	if err != nil {
		fail(w, http.StatusInternalServerError, "handle_failed", err.Error())
		return
	}
	ok(w, http.StatusOK, reply)
}

type sendReq struct {
	Channel   domain.ChannelKind `json:"channel"`
	ChannelID string             `json:"channel_id,omitempty"`
	To        string             `json:"to"`
	Text      string             `json:"text"`
}

// send delivers a message over any connected channel (notifications, statements).
func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var req sendReq
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.To == "" || req.Text == "" {
		fail(w, http.StatusBadRequest, "bad_request", "to and text are required")
		return
	}
	if _, found := s.Channels.Get(req.Channel); !found {
		fail(w, http.StatusBadRequest, "bad_request", "unknown channel")
		return
	}
	c := snapFrom(r).Settings.CompanyID
	if req.ChannelID != "" {
		conn, err := s.Store.GetConnection(r.Context(), req.ChannelID)
		if err != nil || (conn.CompanyID != c && conn.CompanyID != "") {
			fail(w, http.StatusNotFound, "not_found", "channel connection not found")
			return
		}
	}
	id, err := s.Queue.Enqueue(r.Context(), queue.OutboundArgs{Out: domain.OutboundMessage{
		CompanyID: c, Channel: req.Channel, ChannelID: req.ChannelID, To: req.To, Text: req.Text,
	}})
	if err != nil {
		fail(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
		return
	}
	ok(w, http.StatusAccepted, map[string]any{"job_id": id, "status": "queued"})
}

// --- drafts -------------------------------------------------------------------------

func (s *Server) listDrafts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.DraftFilter{Status: domain.DraftStatus(q.Get("status"))}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	if b := q.Get("before"); b != "" {
		t, err := time.Parse(time.RFC3339Nano, b)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "before must be RFC3339")
			return
		}
		f.Before = t
	}
	snap := snapFrom(r)
	if a := q.Get("actor_id"); a != "" {
		f.ActorID = domain.UserID(a)
	}
	list, err := s.Store.ListDrafts(r.Context(), snap.Settings.CompanyID, f)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	next := ""
	if len(list) > 0 && (f.Limit == 0 || len(list) == f.Limit) {
		next = list[len(list)-1].CreatedAt.Format(time.RFC3339Nano)
	}
	ok(w, http.StatusOK, map[string]any{"drafts": list, "next_before": next})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.GetDraft(r.Context(), snapFrom(r).Settings.CompanyID, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found", "draft not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	ok(w, http.StatusOK, d)
}

type decideReq struct {
	ActorID string `json:"actor_id"`
	Lang    string `json:"lang,omitempty"`
}

func (s *Server) decideDraft(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req decideReq
		if err := decode(r, &req); err != nil {
			fail(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		snap := snapFrom(r)
		actor, found := snap.ActorByID(domain.UserID(req.ActorID))
		if !found {
			fail(w, http.StatusUnprocessableEntity, "unknown_actor", "actor_id is not in the permission matrix")
			return
		}
		lang := req.Lang
		if lang == "" {
			lang = "tr"
		}
		var (
			d    domain.Draft
			text string
			err  error
		)
		if approve {
			d, text, err = s.Engine.Approve(r.Context(), snap, actor, chi.URLParam(r, "id"), lang)
		} else {
			d, text, err = s.Engine.Reject(r.Context(), snap, actor, chi.URLParam(r, "id"), lang)
		}
		switch {
		case errors.Is(err, store.ErrNotFound):
			fail(w, http.StatusNotFound, "not_found", "draft not found")
		case errors.Is(err, store.ErrConflict):
			fail(w, http.StatusConflict, "conflict", text)
		case err != nil:
			fail(w, http.StatusInternalServerError, "commit_failed", err.Error())
		default:
			ok(w, http.StatusOK, map[string]any{"draft": d, "text": text})
		}
	}
}

// --- channels --------------------------------------------------------------------------

type channelReq struct {
	ID      string             `json:"id,omitempty"`
	Kind    domain.ChannelKind `json:"kind"`
	Mode    string             `json:"mode,omitempty"`
	Name    string             `json:"name,omitempty"`
	Enabled *bool              `json:"enabled,omitempty"`
	Config  json.RawMessage    `json:"config"`
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListConnections(r.Context(), snapFrom(r).Settings.CompanyID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	for i := range list {
		list[i].Config = nil // secrets never leave the service
	}
	ok(w, http.StatusOK, map[string]any{"channels": list, "webhook_base": strings.TrimRight(s.PublicURL, "/") + "/webhooks"})
}

func (s *Server) upsertChannel(w http.ResponseWriter, r *http.Request) {
	var req channelReq
	if err := decode(r, &req); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, found := s.Channels.Get(req.Kind); !found || req.Kind == domain.ChannelDev {
		fail(w, http.StatusBadRequest, "bad_request", "kind must be one of telegram, wuzapi, waba, email")
		return
	}
	if len(req.Config) == 0 || !json.Valid(req.Config) {
		fail(w, http.StatusBadRequest, "bad_request", "config must be a JSON object")
		return
	}
	c := snapFrom(r).Settings.CompanyID
	conn := store.Connection{ID: req.ID, CompanyID: c, Kind: req.Kind, Mode: req.Mode, Name: req.Name, Enabled: true, Status: "new"}
	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}
	if conn.ID == "" {
		conn.ID = events.NewID("ch")
	} else if existing, err := s.Store.GetConnection(r.Context(), conn.ID); err == nil && existing.CompanyID != c {
		fail(w, http.StatusNotFound, "not_found", "channel connection not found")
		return
	}
	cfg := []byte(req.Config)
	if s.Box != nil {
		sealed, err := s.Box.Seal(cfg)
		if err != nil {
			fail(w, http.StatusInternalServerError, "encrypt_failed", err.Error())
			return
		}
		cfg = sealed
	}
	conn.Config = cfg
	if err := s.Store.UpsertConnection(r.Context(), &conn); err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	conn.Config = nil
	ok(w, http.StatusOK, map[string]any{
		"channel":     conn,
		"webhook_url": strings.TrimRight(s.PublicURL, "/") + "/webhooks/" + string(conn.Kind) + "/" + conn.ID,
	})
}

func (s *Server) ownConn(r *http.Request) (channels.Conn, error) {
	c, err := s.Store.GetConnection(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		return channels.Conn{}, err
	}
	if c.CompanyID != snapFrom(r).Settings.CompanyID {
		return channels.Conn{}, store.ErrNotFound
	}
	return worker.Decrypt(s.Box, c)
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	err := s.Store.DeleteConnection(r.Context(), snapFrom(r).Settings.CompanyID, chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "not_found", "channel connection not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	ok(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) channelStatus(w http.ResponseWriter, r *http.Request) {
	conn, err := s.ownConn(r)
	if err != nil {
		fail(w, http.StatusNotFound, "not_found", "channel connection not found")
		return
	}
	drv, _ := s.Channels.Get(conn.Kind)
	st, err := drv.Status(r.Context(), conn)
	if err != nil {
		fail(w, http.StatusBadGateway, "status_failed", err.Error())
		return
	}
	_ = s.Store.SetConnectionStatus(r.Context(), conn.ID, st)
	ok(w, http.StatusOK, map[string]string{"id": conn.ID, "status": st})
}

// channelAction exposes WuzAPI session controls (QR pairing) without a UI.
func (s *Server) channelAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := s.ownConn(r)
		if err != nil {
			fail(w, http.StatusNotFound, "not_found", "channel connection not found")
			return
		}
		wz, isWuz := mustDriver(s, conn.Kind).(channels.Wuzapi)
		if !isWuz {
			fail(w, http.StatusBadRequest, "unsupported", action+" is only available for wuzapi connections")
			return
		}
		switch action {
		case "connect":
			err = wz.Connect(r.Context(), conn)
			if err == nil {
				ok(w, http.StatusOK, map[string]string{"status": "connecting"})
			}
		case "qr":
			var qr string
			qr, err = wz.QR(r.Context(), conn)
			if err == nil {
				ok(w, http.StatusOK, map[string]string{"qr": qr})
			}
		case "logout":
			err = wz.Logout(r.Context(), conn)
			if err == nil {
				ok(w, http.StatusOK, map[string]string{"status": "logged_out"})
			}
		}
		if err != nil {
			fail(w, http.StatusBadGateway, "wuzapi_error", err.Error())
		}
	}
}

func mustDriver(s *Server, k domain.ChannelKind) channels.Driver {
	d, _ := s.Channels.Get(k)
	return d
}

// channelWebhook receives pushes from Telegram, WuzAPI, Meta and mail providers.
// It acknowledges as soon as the message is durably queued.
func (s *Server) channelWebhook(w http.ResponseWriter, r *http.Request) {
	kind := domain.ChannelKind(chi.URLParam(r, "kind"))
	drv, found := s.Channels.Get(kind)
	if !found {
		fail(w, http.StatusNotFound, "not_found", "unknown channel")
		return
	}
	c, err := s.Store.GetConnection(r.Context(), chi.URLParam(r, "connID"))
	if err != nil || c.Kind != kind || !c.Enabled {
		fail(w, http.StatusNotFound, "not_found", "unknown connection")
		return
	}
	conn, err := worker.Decrypt(s.Box, c)
	if err != nil {
		fail(w, http.StatusInternalServerError, "decrypt_failed", "connection config unreadable")
		return
	}
	msgs, err := drv.Webhook(w, r, conn)
	switch {
	case errors.Is(err, channels.ErrHandled):
		return
	case errors.Is(err, channels.ErrSignature):
		fail(w, http.StatusUnauthorized, "bad_signature", "webhook verification failed")
		return
	case err != nil:
		s.Log.Warn("webhook parse failed", "kind", kind, "conn", c.ID, "err", err)
		fail(w, http.StatusBadRequest, "bad_request", "unparseable webhook")
		return
	}
	for _, m := range msgs {
		if err := worker.Accept(r.Context(), s.Store, s.Queue, m); err != nil {
			fail(w, http.StatusServiceUnavailable, "enqueue_failed", err.Error()) // provider will retry
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// --- events, usage, audit ---------------------------------------------------------------

type eventReq struct {
	ID         string          `json:"id,omitempty"`
	Type       string          `json:"type"`
	OccurredAt time.Time       `json:"occurred_at,omitzero"`
	Data       json.RawMessage `json:"data"`
}

// ingestEvent accepts events from the host (stock.low, invoice.overdue, ...).
func (s *Server) ingestEvent(w http.ResponseWriter, r *http.Request) {
	var req eventReq
	if err := decode(r, &req); err != nil || req.Type == "" {
		fail(w, http.StatusBadRequest, "bad_request", "type is required")
		return
	}
	ev := events.Event{ID: req.ID, Type: req.Type, CompanyID: snapFrom(r).Settings.CompanyID, Source: "host", OccurredAt: req.OccurredAt, Data: req.Data}
	if ev.ID == "" {
		ev.ID = events.NewID("evt")
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now().UTC()
	}
	if len(ev.Data) == 0 {
		ev.Data = json.RawMessage(`{}`)
	}
	if ev.Type == events.SettingsChanged {
		s.Tenants.Invalidate(ev.CompanyID)
	}
	if _, err := s.Queue.Enqueue(r.Context(), queue.EventArgs{Event: ev}); err != nil {
		fail(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
		return
	}
	ok(w, http.StatusAccepted, map[string]string{"id": ev.ID, "status": "queued"})
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	to := now.Add(time.Second)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "from must be YYYY-MM-DD")
			return
		}
		from = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			fail(w, http.StatusBadRequest, "bad_request", "to must be YYYY-MM-DD")
			return
		}
		to = t.Add(24 * time.Hour)
	}
	snap := snapFrom(r)
	rows, err := s.Store.UsageReport(r.Context(), snap.Settings.CompanyID, from, to)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var total float64
	var calls int64
	for _, u := range rows {
		total += u.CostUSD
		calls += u.Calls
	}
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	today, _ := s.Store.CallsSince(r.Context(), snap.Settings.CompanyID, start)
	ok(w, http.StatusOK, map[string]any{
		"from": from.Format("2006-01-02"), "to": to.Add(-time.Second).Format("2006-01-02"),
		"rows": rows, "total_calls": calls, "total_cost_usd": total,
		"quota": map[string]any{"daily_limit": snap.Settings.AIDailyLimit, "used_today": today},
	})
}

func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.Store.ListAudit(r.Context(), snapFrom(r).Settings.CompanyID, before, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	ok(w, http.StatusOK, map[string]any{"entries": list})
}

// --- data deletion ---------------------------------------------------------------------------

type deletionReq struct {
	Channel domain.ChannelKind `json:"channel,omitempty"`
	Subject string             `json:"subject"`
}

func (s *Server) createDeletion(w http.ResponseWriter, r *http.Request) {
	var req deletionReq
	if err := decode(r, &req); err != nil || strings.TrimSpace(req.Subject) == "" {
		fail(w, http.StatusBadRequest, "bad_request", "subject is required")
		return
	}
	d, err := s.newDeletion(r.Context(), snapFrom(r).Settings.CompanyID, req.Channel, req.Subject)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	ok(w, http.StatusAccepted, d)
}

func (s *Server) newDeletion(ctx context.Context, c domain.CompanyID, ch domain.ChannelKind, subject string) (store.DeletionRequest, error) {
	d := store.DeletionRequest{
		ID: events.NewID("del"), CompanyID: c, Channel: ch, Subject: strings.TrimSpace(subject),
		Status: "pending", Code: events.NewID("code")[5:21], RequestedAt: time.Now().UTC(),
	}
	if err := s.Store.CreateDeletion(ctx, d); err != nil {
		return d, err
	}
	_, err := s.Queue.Enqueue(ctx, queue.DeletionArgs{ID: d.ID})
	return d, err
}

func (s *Server) getDeletion(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.GetDeletion(r.Context(), chi.URLParam(r, "id"))
	if err != nil || d.CompanyID != snapFrom(r).Settings.CompanyID {
		fail(w, http.StatusNotFound, "not_found", "deletion request not found")
		return
	}
	ok(w, http.StatusOK, d)
}

// metaDeletion implements Meta's Data Deletion Request Callback:
// signed_request = base64url(sig) "." base64url(payload), sig = HMAC-SHA256(payload, app secret).
func (s *Server) metaDeletion(w http.ResponseWriter, r *http.Request) {
	if s.MetaAppSecret == "" {
		fail(w, http.StatusNotFound, "not_found", "meta callback not configured")
		return
	}
	if err := r.ParseForm(); err != nil {
		fail(w, http.StatusBadRequest, "bad_request", "form expected")
		return
	}
	sigPart, payloadPart, found := strings.Cut(r.PostForm.Get("signed_request"), ".")
	if !found {
		fail(w, http.StatusBadRequest, "bad_request", "signed_request missing")
		return
	}
	sig, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(sigPart, "="))
	payload, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(payloadPart, "="))
	mac := hmac.New(sha256.New, []byte(s.MetaAppSecret))
	mac.Write([]byte(payloadPart))
	if err1 != nil || err2 != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		fail(w, http.StatusUnauthorized, "bad_signature", "signed_request verification failed")
		return
	}
	var p struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(payload, &p); err != nil || p.UserID == "" {
		fail(w, http.StatusBadRequest, "bad_request", "user_id missing")
		return
	}
	d, err := s.newDeletion(r.Context(), "", domain.ChannelWABA, p.UserID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"url":               strings.TrimRight(s.PublicURL, "/") + "/deletion-status/" + d.Code,
		"confirmation_code": d.Code,
	})
}

// deletionStatus is the public status page Meta links to. It reveals only the state.
func (s *Server) deletionStatus(w http.ResponseWriter, r *http.Request) {
	ok(w, http.StatusOK, map[string]string{"confirmation_code": chi.URLParam(r, "code"), "status": "received"})
}

// --- dev channel and simulator -------------------------------------------------------------------

type devReq struct {
	Sender string   `json:"sender"`
	Text   string   `json:"text,omitempty"`
	Media  *mediaIn `json:"media,omitempty"`
	Async  bool     `json:"async,omitempty"`
}

func (s *Server) devMessage(w http.ResponseWriter, r *http.Request) {
	var req devReq
	if err := decode(r, &req); err != nil || req.Sender == "" {
		fail(w, http.StatusBadRequest, "bad_request", "sender is required")
		return
	}
	media, err := req.Media.decode()
	if err != nil {
		fail(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	msg := domain.InboundMessage{
		ID: events.NewID("dev"), CompanyID: snapFrom(r).Settings.CompanyID, Channel: domain.ChannelDev,
		Sender: req.Sender, Kind: kindFor(req.Text, media), Text: req.Text, Media: media, ReceivedAt: time.Now().UTC(),
	}
	if req.Async {
		if err := worker.Accept(r.Context(), s.Store, s.Queue, msg); err != nil {
			fail(w, http.StatusInternalServerError, "enqueue_failed", err.Error())
			return
		}
		ok(w, http.StatusAccepted, map[string]string{"message_id": msg.ID, "status": "queued"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.SyncTimeout)
	defer cancel()
	reply, err := s.Engine.Handle(ctx, msg)
	if err != nil {
		fail(w, http.StatusInternalServerError, "handle_failed", err.Error())
		return
	}
	for _, t := range reply.Texts {
		_ = s.Dev.Send(ctx, channels.Conn{}, domain.OutboundMessage{CompanyID: msg.CompanyID, Channel: domain.ChannelDev, To: msg.Sender, Text: t})
	}
	ok(w, http.StatusOK, reply)
}

func (s *Server) devOutbox(w http.ResponseWriter, r *http.Request) {
	msgs, err := s.Dev.Outbox(r.Context(), snapFrom(r).Settings.CompanyID, r.URL.Query().Get("to"))
	if err != nil {
		fail(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	ok(w, http.StatusOK, map[string]any{"messages": msgs})
}

func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	if s.Simulate == nil {
		fail(w, http.StatusNotImplemented, "unavailable", "simulator disabled")
		return
	}
	n, err := s.Simulate(r.Context(), snapFrom(r).Settings.CompanyID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "simulate_failed", err.Error())
		return
	}
	ok(w, http.StatusOK, map[string]int{"events": n})
}

// --- admin ---------------------------------------------------------------------------------------

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	if s.Jobs == nil {
		fail(w, http.StatusNotImplemented, "unavailable", "job listing disabled")
		return
	}
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "discarded"
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.Jobs(r.Context(), state, limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	ok(w, http.StatusOK, jobs)
}

func (s *Server) adminInvalidate(w http.ResponseWriter, r *http.Request) {
	s.Tenants.Invalidate(domain.CompanyID(chi.URLParam(r, "id")))
	ok(w, http.StatusOK, map[string]bool{"invalidated": true})
}
