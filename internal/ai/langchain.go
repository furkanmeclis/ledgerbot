package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/anthropic"
	"github.com/tmc/langchaingo/llms/ollama"
	"github.com/tmc/langchaingo/llms/openai"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/furkanmeclis/ledgerbot/internal/telemetry"
)

// LangChainLLM adapts any langchaingo model to the LLM interface. OpenAI's
// adapter also covers every OpenAI-compatible API (Gemini, Groq, OpenRouter,
// DeepSeek, vLLM, LM Studio) through BaseURL.
type LangChainLLM struct {
	Model    llms.Model
	Provider string
	ModelID  string
}

// NewLangChainLLM builds a model from a profile.
func NewLangChainLLM(p Profile) (*LangChainLLM, error) {
	var (
		m   llms.Model
		err error
	)
	switch p.Provider {
	case "openai", "openai-compatible", "gemini", "groq", "openrouter", "deepseek":
		opts := []openai.Option{openai.WithModel(p.Model)}
		if p.APIKey != "" {
			opts = append(opts, openai.WithToken(p.APIKey))
		}
		if p.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(p.BaseURL))
		}
		m, err = openai.New(opts...)
	case "anthropic":
		opts := []anthropic.Option{anthropic.WithModel(p.Model)}
		if p.APIKey != "" {
			opts = append(opts, anthropic.WithToken(p.APIKey))
		}
		if p.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(p.BaseURL))
		}
		m, err = anthropic.New(opts...)
	case "ollama":
		opts := []ollama.Option{ollama.WithModel(p.Model)}
		if p.BaseURL != "" {
			opts = append(opts, ollama.WithServerURL(p.BaseURL))
		}
		m, err = ollama.New(opts...)
	default:
		return nil, fmt.Errorf("ai: unknown llm provider %q", p.Provider)
	}
	if err != nil {
		return nil, err
	}
	return &LangChainLLM{Model: m, Provider: p.Provider, ModelID: p.Model}, nil
}

func (l *LangChainLLM) Complete(ctx context.Context, req CompletionRequest) (Completion, error) {
	msgs := make([]llms.MessageContent, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeSystem, req.System))
	}
	for i, m := range req.Messages {
		role := llms.ChatMessageTypeHuman
		if m.Role == "assistant" {
			role = llms.ChatMessageTypeAI
		}
		mc := llms.MessageContent{Role: role, Parts: []llms.ContentPart{llms.TextPart(m.Text)}}
		if i == len(req.Messages)-1 && role == llms.ChatMessageTypeHuman {
			for _, img := range req.Images {
				if img.URL != "" && len(img.Data) == 0 {
					mc.Parts = append(mc.Parts, llms.ImageURLPart(img.URL))
				} else {
					mc.Parts = append(mc.Parts, llms.BinaryPart(img.MIME, img.Data))
				}
			}
		}
		msgs = append(msgs, mc)
	}
	opts := []llms.CallOption{llms.WithTemperature(0)}
	if req.JSON {
		opts = append(opts, llms.WithJSONMode())
	}
	if req.MaxTokens > 0 {
		opts = append(opts, llms.WithMaxTokens(req.MaxTokens))
	}
	ctx, span := telemetry.Tracer().Start(ctx, "llm.complete", trace.WithAttributes(
		attribute.String("gen_ai.system", l.Provider), attribute.String("gen_ai.request.model", l.ModelID)))
	defer span.End()
	start := time.Now()
	resp, err := l.Model.GenerateContent(ctx, msgs, opts...)
	if err != nil {
		span.RecordError(err)
		return Completion{}, err
	}
	if len(resp.Choices) == 0 {
		return Completion{}, fmt.Errorf("ai: empty response from %s", l.Provider)
	}
	ch := resp.Choices[0]
	u := Usage{Provider: l.Provider, Model: l.ModelID, Latency: time.Since(start)}
	u.InputTokens = intInfo(ch.GenerationInfo, "PromptTokens", "InputTokens", "prompt_tokens", "input_tokens")
	u.OutputTokens = intInfo(ch.GenerationInfo, "CompletionTokens", "OutputTokens", "completion_tokens", "output_tokens")
	span.SetAttributes(attribute.Int("gen_ai.usage.input_tokens", u.InputTokens), attribute.Int("gen_ai.usage.output_tokens", u.OutputTokens))
	return Completion{Text: ch.Content, Usage: u}, nil
}

func intInfo(m map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case int:
			return v
		case int32:
			return int(v)
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
	}
	return 0
}
