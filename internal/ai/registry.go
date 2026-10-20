package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// Profile is one named provider configuration.
type Profile struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`     // "llm", "stt", "embedding"
	Provider string `json:"provider"` // openai, anthropic, ollama, gemini, groq, openrouter, fake ...
	Model    string `json:"model"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"` // or api_key_env
	KeyEnv   string `json:"api_key_env,omitempty"`
	// Pricing per million tokens (or per audio minute for STT) in USD micro-cents,
	// used for cost tracking.
	InputPrice  float64 `json:"input_price_per_mtok,omitempty"`
	OutputPrice float64 `json:"output_price_per_mtok,omitempty"`
	MinutePrice float64 `json:"price_per_audio_minute,omitempty"`
	Dimensions  int     `json:"dimensions,omitempty"`
}

// Config is the providers file: named profiles plus a default per purpose.
type Config struct {
	Profiles []Profile          `json:"profiles"`
	Defaults map[Purpose]string `json:"defaults"`
}

// LoadConfig reads a providers JSON file. An empty path yields the fake setup.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		return FakeConfig(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("ai: parse %s: %w", path, err)
	}
	for i := range c.Profiles {
		if c.Profiles[i].APIKey == "" && c.Profiles[i].KeyEnv != "" {
			c.Profiles[i].APIKey = os.Getenv(c.Profiles[i].KeyEnv)
		}
	}
	return c, nil
}

// FakeConfig wires the deterministic fake providers for every purpose.
func FakeConfig() Config {
	return Config{
		Profiles: []Profile{
			{Name: "fake-llm", Kind: "llm", Provider: "fake", Model: "fake"},
			{Name: "fake-stt", Kind: "stt", Provider: "fake", Model: "fake"},
			{Name: "fake-embed", Kind: "embedding", Provider: "fake", Model: "fake", Dimensions: 64},
		},
		Defaults: map[Purpose]string{
			PurposeAgent: "fake-llm", PurposeExtraction: "fake-llm",
			PurposeSTT: "fake-stt", PurposeEmbedding: "fake-embed",
		},
	}
}

// Registry builds providers lazily and caches them; lookups are lock-free after
// first use of a profile (sync.Map).
type Registry struct {
	cfg      Config
	byName   map[string]Profile
	http     *http.Client
	llms     sync.Map // name -> LLM
	stts     sync.Map // name -> STT
	embedder sync.Map // name -> Embedder
}

// NewRegistry validates the config and returns a registry.
func NewRegistry(cfg Config) (*Registry, error) {
	r := &Registry{cfg: cfg, byName: map[string]Profile{}, http: &http.Client{Timeout: 120 * time.Second}}
	for _, p := range cfg.Profiles {
		r.byName[p.Name] = p
	}
	for purpose, name := range cfg.Defaults {
		if _, ok := r.byName[name]; !ok {
			return nil, fmt.Errorf("ai: default %s refers to unknown profile %q", purpose, name)
		}
	}
	return r, nil
}

func (r *Registry) profile(purpose Purpose, override string) (Profile, error) {
	name := override
	if name == "" {
		name = r.cfg.Defaults[purpose]
	}
	p, ok := r.byName[name]
	if !ok {
		return Profile{}, fmt.Errorf("%w: %s (%q)", ErrUnavailable, purpose, name)
	}
	return p, nil
}

// Profile returns the resolved profile, for pricing lookups.
func (r *Registry) Profile(purpose Purpose, override string) (Profile, error) {
	return r.profile(purpose, override)
}

// LLM returns the model for a purpose; override is an optional company profile name.
func (r *Registry) LLM(purpose Purpose, override string) (LLM, error) {
	p, err := r.profile(purpose, override)
	if err != nil {
		return nil, err
	}
	if v, ok := r.llms.Load(p.Name); ok {
		return v.(LLM), nil
	}
	var m LLM
	if p.Provider == "fake" {
		m = FakeLLM{}
	} else if m, err = NewLangChainLLM(p); err != nil {
		return nil, err
	}
	v, _ := r.llms.LoadOrStore(p.Name, m)
	return v.(LLM), nil
}

// STT returns the speech-to-text provider.
func (r *Registry) STT(override string) (STT, error) {
	p, err := r.profile(PurposeSTT, override)
	if err != nil {
		return nil, err
	}
	if v, ok := r.stts.Load(p.Name); ok {
		return v.(STT), nil
	}
	var s STT
	switch p.Provider {
	case "fake":
		s = FakeSTT{}
	case "openai", "openai-compatible", "groq":
		s = &OpenAISTT{BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model, Provider: p.Provider, Client: r.http}
	default:
		return nil, fmt.Errorf("ai: unknown stt provider %q", p.Provider)
	}
	v, _ := r.stts.LoadOrStore(p.Name, s)
	return v.(STT), nil
}

// Embedder returns the embedding provider.
func (r *Registry) Embedder(override string) (Embedder, error) {
	p, err := r.profile(PurposeEmbedding, override)
	if err != nil {
		return nil, err
	}
	if v, ok := r.embedder.Load(p.Name); ok {
		return v.(Embedder), nil
	}
	var e Embedder
	switch p.Provider {
	case "fake":
		e = FakeEmbedder{Dims: max(p.Dimensions, 8)}
	case "openai", "openai-compatible", "ollama", "gemini":
		e = &OpenAIEmbedder{BaseURL: p.BaseURL, APIKey: p.APIKey, Model: p.Model, Dims: p.Dimensions, Provider: p.Provider, Client: r.http}
	default:
		return nil, fmt.Errorf("ai: unknown embedding provider %q", p.Provider)
	}
	v, _ := r.embedder.LoadOrStore(p.Name, e)
	return v.(Embedder), nil
}

// CostMicroUSD estimates the cost of a usage record under a profile.
func CostMicroUSD(p Profile, u Usage) int64 {
	c := float64(u.InputTokens)*p.InputPrice + float64(u.OutputTokens)*p.OutputPrice // price per Mtok -> micro-USD per token
	c += u.AudioSeconds / 60 * p.MinutePrice * 1e6
	return int64(c)
}
