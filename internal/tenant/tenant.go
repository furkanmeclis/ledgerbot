// Package tenant implements two of ledgerbot's external contracts:
//
//   - Settings: per-company configuration (save mode, providers, webhooks, API keys).
//   - Permission matrix: roles, their capabilities and the users mapped to them.
//
// Both are owned by the host system and fetched over HTTP (or from seed files in
// development). ledgerbot never edits them; it only reads, caches and enforces.
package tenant

import (
	"context"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// ErrUnknownCompany is returned when the source has no data for a company.
var ErrUnknownCompany = errors.New("tenant: unknown company")

// Settings is the per-company configuration contract (docs/contracts/settings.schema.json).
type Settings struct {
	CompanyID    domain.CompanyID `json:"company_id"`
	Version      int64            `json:"version"`
	SaveMode     domain.SaveMode  `json:"save_mode"`
	Locale       string           `json:"locale,omitempty"`
	AIDailyLimit int              `json:"ai_daily_limit,omitempty"`
	Providers    ProviderSettings `json:"providers,omitzero"`
	APIKeys      []APIKey         `json:"api_keys,omitempty"`
	SigningKey   string           `json:"signing_secret,omitempty"` // optional HMAC secret for REST calls
	Webhooks     []Webhook        `json:"webhooks,omitempty"`
	Rules        []Rule           `json:"rules,omitempty"`
	Prompts      Prompts          `json:"prompts,omitzero"`
}

// Prompts lets the host steer the assistant's tone and domain knowledge.
// They are appended to ledgerbot's fixed instructions; they can add context
// (sector, vocabulary, house rules) but cannot change the output contract.
type Prompts struct {
	System string            `json:"system,omitempty"` // company-wide instruction
	Roles  map[string]string `json:"roles,omitempty"`  // extra instruction per role
}

// SystemPrompt returns the combined company and role instruction.
func (s *Snapshot) SystemPrompt(role string) string {
	p := s.Settings.Prompts
	switch r := p.Roles[role]; {
	case p.System != "" && r != "":
		return p.System + "\n" + r
	case r != "":
		return r
	default:
		return p.System
	}
}

// ProviderSettings lets a company override the global provider choice per purpose.
// Empty values mean "use the deployment default".
type ProviderSettings struct {
	Agent      string `json:"agent,omitempty"`      // provider profile name, e.g. "openai-mini"
	Extraction string `json:"extraction,omitempty"` // used for receipts / documents (vision)
	STT        string `json:"stt,omitempty"`
}

// APIKey is a hashed REST credential. The plain key is never sent to ledgerbot
// after issuance; only its SHA-256 hex digest.
type APIKey struct {
	ID     string   `json:"id"`
	SHA256 string   `json:"sha256"`
	Scopes []string `json:"scopes,omitempty"`
}

// Webhook is an outbound event subscription.
type Webhook struct {
	URL    string   `json:"url"`
	Secret string   `json:"secret"`
	Events []string `json:"events"` // glob patterns: "draft.*", "*"
}

// Rule binds an event type to a proactive notification.
type Rule struct {
	Event       string   `json:"event"`              // e.g. "stock.low"
	NotifyRoles []string `json:"notify_roles"`       // roles to notify
	Throttle    string   `json:"throttle,omitempty"` // Go duration, e.g. "6h"
}

// Matrix is the permission matrix contract (docs/contracts/permissions.schema.json).
type Matrix struct {
	CompanyID domain.CompanyID `json:"company_id"`
	Version   int64            `json:"version"`
	Roles     map[string]Role  `json:"roles"`
	Users     []User           `json:"users"`
}

// Role describes what a role may do through ledgerbot.
type Role struct {
	Capabilities []string             `json:"capabilities"`        // capability names or globs: "query_*", "*"
	Channels     []domain.ChannelKind `json:"channels,omitempty"`  // empty = all channels
	SaveMode     domain.SaveMode      `json:"save_mode,omitempty"` // overrides company save mode (can only be stricter)
	Scope        string               `json:"scope,omitempty"`     // "all" (default) or "own"
}

// User maps a host user to a role and to their channel addresses.
type User struct {
	ID        domain.UserID                 `json:"id"`
	Name      string                        `json:"name,omitempty"`
	Role      string                        `json:"role"`
	Addresses map[domain.ChannelKind]string `json:"addresses"`
}

// Snapshot is the combined, immutable view used while handling one request.
type Snapshot struct {
	Settings  Settings
	Matrix    Matrix
	FetchedAt time.Time
	// Degraded is true when the source was unreachable and ledgerbot fell back
	// to a stale or safe snapshot. Writes are disabled in degraded mode.
	Degraded bool

	addrIndex map[string]*User // channel|address -> user
}

// NewSnapshot builds a snapshot and its lookup indexes.
func NewSnapshot(s Settings, m Matrix) *Snapshot {
	snap := &Snapshot{Settings: s, Matrix: m, FetchedAt: time.Now(), addrIndex: make(map[string]*User, len(m.Users)*2)}
	for i := range m.Users {
		u := &m.Users[i]
		for ch, addr := range u.Addresses {
			snap.addrIndex[addrKey(ch, addr)] = u
		}
	}
	return snap
}

func addrKey(ch domain.ChannelKind, addr string) string {
	// WhatsApp channels share one address space (phone numbers).
	if ch == domain.ChannelWABA {
		ch = domain.ChannelWuzapi
	}
	return string(ch) + "|" + strings.ToLower(strings.TrimSpace(addr))
}

// ResolveActor maps a channel address to a known user. Unknown senders get ok=false.
// REST API calls use the synthetic address "user:<id>" on the api channel.
func (s *Snapshot) ResolveActor(ch domain.ChannelKind, address string) (domain.Actor, bool) {
	if ch == domain.ChannelAPI {
		if id, ok := strings.CutPrefix(address, "user:"); ok {
			return s.ActorByID(domain.UserID(id))
		}
		return domain.Actor{}, false
	}
	u, ok := s.addrIndex[addrKey(ch, address)]
	if !ok {
		return domain.Actor{}, false
	}
	return domain.Actor{CompanyID: s.Settings.CompanyID, UserID: u.ID, Role: u.Role, Name: u.Name}, true
}

// ActorByID returns the user with the given id (used by the REST API).
func (s *Snapshot) ActorByID(id domain.UserID) (domain.Actor, bool) {
	for _, u := range s.Matrix.Users {
		if u.ID == id {
			return domain.Actor{CompanyID: s.Settings.CompanyID, UserID: u.ID, Role: u.Role, Name: u.Name}, true
		}
	}
	return domain.Actor{}, false
}

// UsersWithRoles lists users holding any of the given roles.
func (s *Snapshot) UsersWithRoles(roles []string) []User {
	var out []User
	for _, u := range s.Matrix.Users {
		for _, r := range roles {
			if u.Role == r {
				out = append(out, u)
				break
			}
		}
	}
	return out
}

// Subscribed reports whether any webhook subscribes to an event type.
func (s *Snapshot) Subscribed(eventType string) bool {
	for _, wh := range s.Settings.Webhooks {
		for _, p := range wh.Events {
			if Match(p, eventType) {
				return true
			}
		}
	}
	return false
}

// Allowed reports whether role may use capability over channel.
func (s *Snapshot) Allowed(role, capability string, ch domain.ChannelKind) bool {
	r, ok := s.Matrix.Roles[role]
	if !ok {
		return false
	}
	if len(r.Channels) > 0 && ch != domain.ChannelAPI {
		found := false
		for _, c := range r.Channels {
			if c == ch {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, pat := range r.Capabilities {
		if Match(pat, capability) {
			return true
		}
	}
	return false
}

// OwnScope reports whether the role only sees its own records.
func (s *Snapshot) OwnScope(role string) bool {
	return s.Matrix.Roles[role].Scope == "own"
}

// EffectiveSaveMode combines the company mode with a role override. A role can
// only make the mode stricter, never looser. Degraded snapshots never write.
func (s *Snapshot) EffectiveSaveMode(role string) domain.SaveMode {
	if s.Degraded {
		return domain.SaveReplyOnly
	}
	mode := s.Settings.SaveMode
	if !mode.Valid() {
		mode = domain.SaveReplyOnly
	}
	if r, ok := s.Matrix.Roles[role]; ok && r.SaveMode.Valid() && strictness(r.SaveMode) > strictness(mode) {
		mode = r.SaveMode
	}
	return mode
}

func strictness(m domain.SaveMode) int {
	switch m {
	case domain.SaveAuto:
		return 0
	case domain.SaveAskApproval:
		return 1
	default:
		return 2
	}
}

// Match is a tiny glob matcher for capability and event patterns.
func Match(pattern, name string) bool {
	if pattern == "*" || pattern == name {
		return true
	}
	ok, _ := path.Match(pattern, name)
	return ok
}

// Source fetches tenant data from the host system.
type Source interface {
	Settings(ctx context.Context, company domain.CompanyID) (Settings, error)
	Matrix(ctx context.Context, company domain.CompanyID) (Matrix, error)
}

// Directory routes a channel address to companies. It is used by central
// connections (one shared WhatsApp number or bot for many companies).
type Directory interface {
	Lookup(ctx context.Context, ch domain.ChannelKind, address string) ([]domain.CompanyID, error)
}
