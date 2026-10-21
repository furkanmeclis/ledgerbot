package ai

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestLoadConfigEmptyPathIsFake(t *testing.T) {
	t.Parallel()
	c, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Profiles) != 3 || c.Defaults[PurposeAgent] != "fake-llm" || c.Defaults[PurposeEmbedding] != "fake-embed" {
		t.Fatalf("fake config = %+v", c)
	}
	for _, p := range []Purpose{PurposeAgent, PurposeExtraction, PurposeSTT, PurposeEmbedding} {
		if c.Defaults[p] == "" {
			t.Fatalf("no default for %s", p)
		}
	}
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "providers.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfigFile(t *testing.T) {
	t.Setenv("LEDGERBOT_TEST_OPENAI_KEY", "sk-from-env")
	path := writeFile(t, `{
	  "profiles": [
	    {"name":"gpt","kind":"llm","provider":"openai","model":"gpt-x","api_key_env":"LEDGERBOT_TEST_OPENAI_KEY","input_price_per_mtok":2.5,"output_price_per_mtok":10},
	    {"name":"inline","kind":"llm","provider":"openai","model":"m","api_key":"sk-inline","api_key_env":"LEDGERBOT_TEST_OPENAI_KEY"},
	    {"name":"missing","kind":"llm","provider":"openai","model":"m","api_key_env":"LEDGERBOT_TEST_UNSET_KEY"},
	    {"name":"whisper","kind":"stt","provider":"groq","model":"w","base_url":"http://stt","price_per_audio_minute":0.006},
	    {"name":"emb","kind":"embedding","provider":"ollama","model":"e","dimensions":768}
	  ],
	  "defaults": {"agent":"gpt","stt":"whisper","embedding":"emb"}
	}`)
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, p := range c.Profiles {
		keys[p.Name] = p.APIKey
	}
	if keys["gpt"] != "sk-from-env" || keys["inline"] != "sk-inline" || keys["missing"] != "" {
		t.Fatalf("api keys = %v", keys)
	}
	if c.Profiles[0].InputPrice != 2.5 || c.Profiles[3].MinutePrice != 0.006 || c.Profiles[4].Dimensions != 768 || c.Profiles[3].BaseURL != "http://stt" {
		t.Fatalf("profiles = %+v", c.Profiles)
	}
	if c.Defaults[PurposeSTT] != "whisper" {
		t.Fatalf("defaults = %v", c.Defaults)
	}
}

func TestLoadConfigErrors(t *testing.T) {
	t.Parallel()
	if _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file err = %v", err)
	}
	path := writeFile(t, `{"profiles":[`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("bad json err = %v", err)
	}
}

func TestNewRegistryValidatesDefaults(t *testing.T) {
	t.Parallel()
	if _, err := NewRegistry(FakeConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRegistry(Config{}); err != nil {
		t.Fatalf("empty config: %v", err)
	}
	_, err := NewRegistry(Config{Defaults: map[Purpose]string{PurposeAgent: "ghost"}})
	if err == nil || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v", err)
	}
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	cfg := FakeConfig()
	cfg.Profiles = append(cfg.Profiles,
		Profile{Name: "oa", Kind: "llm", Provider: "openai", Model: "gpt-test", APIKey: "sk-test", BaseURL: "http://127.0.0.1:1/v1"},
		Profile{Name: "claude", Kind: "llm", Provider: "anthropic", Model: "c", APIKey: "k", BaseURL: "http://127.0.0.1:1"},
		Profile{Name: "local", Kind: "llm", Provider: "ollama", Model: "llama", BaseURL: "http://127.0.0.1:1"},
		Profile{Name: "weird-llm", Kind: "llm", Provider: "carrier-pigeon"},
		Profile{Name: "whisper", Kind: "stt", Provider: "openai", Model: "whisper-1", APIKey: "k", BaseURL: "http://x"},
		Profile{Name: "groq-stt", Kind: "stt", Provider: "groq", Model: "w"},
		Profile{Name: "bad-stt", Kind: "stt", Provider: "ollama"},
		Profile{Name: "oa-emb", Kind: "embedding", Provider: "openai-compatible", Model: "e", Dimensions: 256},
		Profile{Name: "tiny-fake", Kind: "embedding", Provider: "fake", Dimensions: 2},
		Profile{Name: "bad-emb", Kind: "embedding", Provider: "anthropic"},
	)
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistryLLM(t *testing.T) {
	t.Parallel()
	r := testRegistry(t)
	m, err := r.LLM(PurposeAgent, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(FakeLLM); !ok {
		t.Fatalf("default agent = %T", m)
	}
	for _, name := range []string{"oa", "claude", "local"} {
		m, err := r.LLM(PurposeAgent, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		lc, ok := m.(*LangChainLLM)
		if !ok {
			t.Fatalf("%s = %T", name, m)
		}
		again, _ := r.LLM(PurposeExtraction, name)
		if again != m {
			t.Fatalf("%s not cached", name)
		}
		if lc.Provider == "" {
			t.Fatalf("%s provider empty", name)
		}
	}
	if _, err := r.LLM(PurposeAgent, "weird-llm"); err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("unknown provider err = %v", err)
	}
	if _, err := r.LLM(PurposeAgent, "nope"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unknown profile err = %v", err)
	}
}

func TestRegistrySTT(t *testing.T) {
	t.Parallel()
	r := testRegistry(t)
	s, err := r.STT("")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.(FakeSTT); !ok {
		t.Fatalf("default stt = %T", s)
	}
	s, err = r.STT("whisper")
	if err != nil {
		t.Fatal(err)
	}
	o, ok := s.(*OpenAISTT)
	if !ok || o.Model != "whisper-1" || o.BaseURL != "http://x" || o.APIKey != "k" || o.Client == nil {
		t.Fatalf("whisper = %#v", s)
	}
	if again, _ := r.STT("whisper"); again != s {
		t.Fatal("stt not cached")
	}
	if g, err := r.STT("groq-stt"); err != nil || g.(*OpenAISTT).Provider != "groq" {
		t.Fatalf("groq = %#v, %v", g, err)
	}
	if _, err := r.STT("bad-stt"); err == nil {
		t.Fatal("ollama stt should be rejected")
	}
	if _, err := r.STT("ghost"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRegistryEmbedder(t *testing.T) {
	t.Parallel()
	r := testRegistry(t)
	e, err := r.Embedder("")
	if err != nil || e.Dimensions() != 64 {
		t.Fatalf("default embedder = %#v, %v", e, err)
	}
	if e, _ := r.Embedder("tiny-fake"); e.Dimensions() != 8 {
		t.Fatalf("fake dims should be clamped to 8, got %d", e.Dimensions())
	}
	e, err = r.Embedder("oa-emb")
	if err != nil {
		t.Fatal(err)
	}
	if o, ok := e.(*OpenAIEmbedder); !ok || o.Dims != 256 || o.Provider != "openai-compatible" {
		t.Fatalf("oa-emb = %#v", e)
	}
	if again, _ := r.Embedder("oa-emb"); again != e {
		t.Fatal("embedder not cached")
	}
	if _, err := r.Embedder("bad-emb"); err == nil {
		t.Fatal("anthropic embedder should be rejected")
	}
}

func TestRegistryMissingDefault(t *testing.T) {
	t.Parallel()
	r, err := NewRegistry(Config{Profiles: []Profile{{Name: "x", Provider: "fake"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.LLM(PurposeAgent, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Embedder(""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	p, err := r.Profile(PurposeAgent, "x")
	if err != nil || p.Name != "x" {
		t.Fatalf("Profile = %+v, %v", p, err)
	}
}

func TestRegistryConcurrentLookups(t *testing.T) {
	t.Parallel()
	r := testRegistry(t)
	var wg sync.WaitGroup
	got := make([]LLM, 32)
	for i := range got {
		wg.Go(func() {
			m, err := r.LLM(PurposeAgent, "oa")
			if err != nil {
				t.Error(err)
			}
			got[i] = m
			_, _ = r.STT("whisper")
			_, _ = r.Embedder("oa-emb")
		})
	}
	wg.Wait()
	for _, m := range got {
		if m != got[0] {
			t.Fatal("concurrent lookups returned different instances")
		}
	}
}

func TestCostMicroUSD(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		p    Profile
		u    Usage
		want int64
	}{
		{"zero", Profile{}, Usage{InputTokens: 1000}, 0},
		{"1M input at $3", Profile{InputPrice: 3}, Usage{InputTokens: 1_000_000}, 3_000_000},
		{"in and out", Profile{InputPrice: 2.5, OutputPrice: 10}, Usage{InputTokens: 1200, OutputTokens: 300}, 3000 + 3000},
		{"one minute audio", Profile{MinutePrice: 0.006}, Usage{AudioSeconds: 60}, 6000},
		{"thirty seconds", Profile{MinutePrice: 0.006}, Usage{AudioSeconds: 30}, 3000},
		{"truncates", Profile{InputPrice: 0.15}, Usage{InputTokens: 3}, 0},
	}
	for _, c := range cases {
		if got := CostMicroUSD(c.p, c.u); got != c.want {
			t.Errorf("%s: CostMicroUSD = %d, want %d", c.name, got, c.want)
		}
	}
}
