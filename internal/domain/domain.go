// Package domain holds the core types shared by every layer of ledgerbot.
// It has no dependencies on transport, storage or providers.
package domain

import (
	"encoding/json"
	"time"
)

// CompanyID identifies a tenant. It is opaque to ledgerbot and owned by the host
// accounting system.
type CompanyID string

// UserID identifies a user inside a company, as known by the host system.
type UserID string

// ChannelKind names a channel family.
type ChannelKind string

const (
	ChannelDev      ChannelKind = "dev"
	ChannelTelegram ChannelKind = "telegram"
	ChannelWuzapi   ChannelKind = "wuzapi"
	ChannelWABA     ChannelKind = "waba"
	ChannelEmail    ChannelKind = "email"
	ChannelAPI      ChannelKind = "api"
)

// SaveMode decides what happens after a write intent has been understood.
type SaveMode string

const (
	// SaveReplyOnly never writes; the assistant only explains what it would record.
	SaveReplyOnly SaveMode = "reply_only"
	// SaveAskApproval stores a pending draft and waits for an explicit yes/no.
	SaveAskApproval SaveMode = "ask_approval"
	// SaveAuto commits the draft to the data provider immediately.
	SaveAuto SaveMode = "auto_save"
)

// Valid reports whether m is a known save mode.
func (m SaveMode) Valid() bool {
	switch m {
	case SaveReplyOnly, SaveAskApproval, SaveAuto:
		return true
	}
	return false
}

// MessageKind is the normalized payload type of an inbound message.
type MessageKind string

const (
	KindText     MessageKind = "text"
	KindImage    MessageKind = "image"
	KindAudio    MessageKind = "audio"
	KindDocument MessageKind = "document"
)

// Media is an attachment carried by a message. Data is kept in memory only for
// the lifetime of a job; ledgerbot never stores raw media by default.
type Media struct {
	MIME     string `json:"mime"`
	Filename string `json:"filename,omitempty"`
	Data     []byte `json:"data,omitempty"`
	URL      string `json:"url,omitempty"`
}

// InboundMessage is the channel-independent form of anything a user sends.
type InboundMessage struct {
	ID         string      `json:"id"`
	CompanyID  CompanyID   `json:"company_id"`
	Channel    ChannelKind `json:"channel"`
	ChannelID  string      `json:"channel_id,omitempty"` // connection id for per-company channels
	Sender     string      `json:"sender"`               // channel address: E.164 phone, telegram chat id, email
	Kind       MessageKind `json:"kind"`
	Text       string      `json:"text,omitempty"`
	Media      *Media      `json:"media,omitempty"`
	ReceivedAt time.Time   `json:"received_at"`
}

// OutboundMessage is what the assistant sends back over a channel.
type OutboundMessage struct {
	CompanyID CompanyID   `json:"company_id"`
	Channel   ChannelKind `json:"channel"`
	ChannelID string      `json:"channel_id,omitempty"`
	To        string      `json:"to"`
	Text      string      `json:"text"`
	Media     *Media      `json:"media,omitempty"`
}

// Actor is the resolved sender: who they are and what role they hold.
type Actor struct {
	CompanyID CompanyID `json:"company_id"`
	UserID    UserID    `json:"user_id"`
	Role      string    `json:"role"`
	Name      string    `json:"name,omitempty"`
}

// DraftStatus tracks a draft through its lifecycle.
type DraftStatus string

const (
	DraftPreview   DraftStatus = "preview"   // reply_only: shown, never committed
	DraftPending   DraftStatus = "pending"   // ask_approval: waiting for the user
	DraftCommitted DraftStatus = "committed" // written to the data provider
	DraftRejected  DraftStatus = "rejected"
	DraftFailed    DraftStatus = "failed"
)

// Draft is a structured record the assistant wants to write.
type Draft struct {
	ID         string          `json:"id"`
	CompanyID  CompanyID       `json:"company_id"`
	ActorID    UserID          `json:"actor_id"`
	Capability string          `json:"capability"`
	Status     DraftStatus     `json:"status"`
	Payload    json.RawMessage `json:"payload"`
	Summary    string          `json:"summary"`
	ExternalID string          `json:"external_id,omitempty"` // id assigned by the data provider on commit
	Channel    ChannelKind     `json:"channel"`
	Sender     string          `json:"sender"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}
