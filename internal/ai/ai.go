// Package ai defines ledgerbot's pluggable AI providers: chat/completion
// models, speech-to-text, document vision and embeddings. Every capability is
// an interface; concrete providers are selected by configuration per purpose
// (agent, extraction, stt, embedding) and optionally per company.
package ai

import (
	"context"
	"errors"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// Purpose selects which provider profile handles a call.
type Purpose string

const (
	PurposeAgent      Purpose = "agent"      // intent understanding, short replies
	PurposeExtraction Purpose = "extraction" // receipts, invoices, documents (vision)
	PurposeSTT        Purpose = "stt"        // voice notes
	PurposeEmbedding  Purpose = "embedding"  // RAG
)

// ErrUnavailable means no provider is configured for a purpose.
var ErrUnavailable = errors.New("ai: provider unavailable")

// Usage reports consumption for cost tracking.
type Usage struct {
	Provider     string        `json:"provider"`
	Model        string        `json:"model"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	AudioSeconds float64       `json:"audio_seconds,omitempty"`
	Latency      time.Duration `json:"latency"`
}

// Message is one chat turn.
type Message struct {
	Role string // "system", "user", "assistant"
	Text string
}

// CompletionRequest asks a model for text or JSON.
type CompletionRequest struct {
	System    string
	Messages  []Message
	Images    []domain.Media // inline images / PDFs for multimodal models
	JSON      bool
	MaxTokens int
}

// Completion is the model's answer.
type Completion struct {
	Text  string
	Usage Usage
}

// LLM generates completions.
type LLM interface {
	Complete(ctx context.Context, req CompletionRequest) (Completion, error)
}

// Transcript is the output of speech-to-text.
type Transcript struct {
	Text     string
	Language string
	Usage    Usage
}

// STT turns audio into text.
type STT interface {
	Transcribe(ctx context.Context, audio domain.Media, languageHint string) (Transcript, error)
}

// Embedder turns text into vectors for RAG.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, Usage, error)
	Dimensions() int
}
