package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

type sttCapture struct {
	path, auth, filename, model, format, lang string
	hasLang                                   bool
	data                                      []byte
}

func sttServer(t *testing.T, status int, body string, got *sttCapture) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.auth = r.URL.Path, r.Header.Get("Authorization")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		f, h, err := r.FormFile("file")
		if err == nil {
			got.filename = h.Filename
			got.data, _ = io.ReadAll(f)
		}
		got.model, got.format = r.FormValue("model"), r.FormValue("response_format")
		_, got.hasLang = r.MultipartForm.Value["language"]
		got.lang = r.FormValue("language")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAISTTTranscribe(t *testing.T) {
	t.Parallel()
	var got sttCapture
	srv := sttServer(t, http.StatusOK, `{"text":"Ahmet'ten beş bin lira","language":"turkish","duration":3.5}`, &got)
	s := &OpenAISTT{BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Model: "whisper-1", Provider: "openai", Client: srv.Client()}
	tr, err := s.Transcribe(context.Background(), domain.Media{MIME: "audio/ogg; codecs=opus", Data: []byte("OggS...")}, "tr")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Text != "Ahmet'ten beş bin lira" || tr.Language != "turkish" || tr.Usage.AudioSeconds != 3.5 || tr.Usage.Provider != "openai" || tr.Usage.Model != "whisper-1" {
		t.Fatalf("transcript = %+v", tr)
	}
	if got.path != "/v1/audio/transcriptions" || got.auth != "Bearer sk-test" || got.filename != "audio.ogg" ||
		got.model != "whisper-1" || got.format != "verbose_json" || got.lang != "tr" || string(got.data) != "OggS..." {
		t.Fatalf("request = %+v", got)
	}
}

func TestOpenAISTTOptionalFields(t *testing.T) {
	t.Parallel()
	var got sttCapture
	srv := sttServer(t, http.StatusOK, `{"text":"hi"}`, &got)
	s := &OpenAISTT{BaseURL: srv.URL, Model: "w", Provider: "groq", Client: srv.Client()}
	if _, err := s.Transcribe(context.Background(), domain.Media{Filename: "note.m4a", MIME: "audio/ogg", Data: []byte("x")}, ""); err != nil {
		t.Fatal(err)
	}
	if got.auth != "" || got.hasLang || got.filename != "note.m4a" || got.path != "/audio/transcriptions" {
		t.Fatalf("request = %+v", got)
	}
}

func TestOpenAISTTErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, body, want string
		status           int
	}{
		{"rate limited", `{"error":"slow down"}`, "stt status 429", http.StatusTooManyRequests},
		{"server error", strings.Repeat("x", 5000), "stt status 500", http.StatusInternalServerError},
		{"bad json", `not json`, "invalid character", http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			var got sttCapture
			srv := sttServer(t, c.status, c.body, &got)
			s := &OpenAISTT{BaseURL: srv.URL, Model: "w", Client: srv.Client()}
			_, err := s.Transcribe(context.Background(), domain.Media{Data: []byte("x")}, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
			if len(err.Error()) > 2200 {
				t.Fatalf("error body not truncated: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestOpenAISTTTransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	s := &OpenAISTT{BaseURL: url, Client: http.DefaultClient}
	if _, err := s.Transcribe(context.Background(), domain.Media{Data: []byte("x")}, ""); err == nil {
		t.Fatal("expected connection error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Transcribe(ctx, domain.Media{Data: []byte("x")}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestAudioExt(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"audio/ogg": ".ogg", "audio/ogg; codecs=opus": ".ogg", "audio/opus": ".ogg",
		"audio/mpeg": ".mp3", "audio/mp3": ".mp3", "audio/wav": ".wav", "audio/x-wav": ".wav",
		"audio/mp4": ".m4a", "audio/x-m4a": ".m4a", "audio/aac": ".m4a", "audio/webm": ".webm",
		"": ".ogg", "application/octet-stream": ".ogg",
	}
	for in, want := range cases {
		if got := audioExt(in); got != want {
			t.Errorf("audioExt(%q) = %q, want %q", in, got, want)
		}
	}
}

func embedServer(t *testing.T, status int, body string, gotReq *map[string]any, gotAuth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("path %s, content-type %s", r.URL.Path, r.Header.Get("Content-Type"))
		}
		if gotAuth != nil {
			*gotAuth = r.Header.Get("Authorization")
		}
		if gotReq != nil {
			_ = json.NewDecoder(r.Body).Decode(gotReq)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestOpenAIEmbedder(t *testing.T) {
	t.Parallel()
	var req map[string]any
	var auth string
	// Out of order, one out-of-range index and one missing entry.
	body := `{"data":[{"index":2,"embedding":[0.3]},{"index":0,"embedding":[0.1,0.2]},{"index":7,"embedding":[9]},{"index":-1,"embedding":[9]}],"usage":{"prompt_tokens":12}}`
	srv := embedServer(t, http.StatusOK, body, &req, &auth)
	e := &OpenAIEmbedder{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "text-embedding-3-small", Dims: 2, Provider: "openai", Client: srv.Client()}
	if e.Dimensions() != 2 {
		t.Fatal("Dimensions")
	}
	vecs, u, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != 3 || len(vecs[0]) != 2 || vecs[0][1] != 0.2 || vecs[1] != nil || vecs[2][0] != 0.3 {
		t.Fatalf("vecs = %v", vecs)
	}
	if u.InputTokens != 12 || u.Provider != "openai" || u.Model != "text-embedding-3-small" {
		t.Fatalf("usage = %+v", u)
	}
	if auth != "Bearer k" || req["model"] != "text-embedding-3-small" || req["dimensions"] != float64(2) {
		t.Fatalf("request = %v auth %q", req, auth)
	}
	if in, _ := req["input"].([]any); len(in) != 3 || in[2] != "c" {
		t.Fatalf("input = %v", req["input"])
	}
}

func TestOpenAIEmbedderNoDimsNoKey(t *testing.T) {
	t.Parallel()
	var req map[string]any
	var auth string
	srv := embedServer(t, http.StatusOK, `{"data":[]}`, &req, &auth)
	e := &OpenAIEmbedder{BaseURL: srv.URL + "/v1/", Model: "nomic", Provider: "ollama", Client: srv.Client()}
	vecs, _, err := e.Embed(context.Background(), []string{"x"})
	if err != nil || len(vecs) != 1 || vecs[0] != nil {
		t.Fatalf("vecs = %v, %v", vecs, err)
	}
	if _, ok := req["dimensions"]; ok || auth != "" {
		t.Fatalf("request = %v auth %q", req, auth)
	}
}

func TestOpenAIEmbedderErrors(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{http.StatusUnauthorized, `{"error":"bad key"}`, "embeddings status 401"},
		{http.StatusOK, `{"data":`, "unexpected EOF"},
	} {
		srv := embedServer(t, c.status, c.body, nil, nil)
		e := &OpenAIEmbedder{BaseURL: srv.URL + "/v1", Client: srv.Client()}
		if _, _, err := e.Embed(context.Background(), []string{"x"}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("err = %v, want %q", err, c.want)
		}
	}
}

func TestLangChainOpenAICompatible(t *testing.T) {
	t.Parallel()
	var req struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens      int             `json:"max_tokens"`
		ResponseFormat json.RawMessage `json:"response_format"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer sk-lc" {
			t.Errorf("path %s auth %s", r.URL.Path, r.Header.Get("Authorization"))
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","created":1,"model":"gpt-test",
			"choices":[{"index":0,"message":{"role":"assistant","content":"{\"capability\":\"help\"}"},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":42,"completion_tokens":7,"total_tokens":49}}`)
	}))
	t.Cleanup(srv.Close)
	llm, err := NewLangChainLLM(Profile{Provider: "openai-compatible", Model: "gpt-test", APIKey: "sk-lc", BaseURL: srv.URL + "/v1"})
	if err != nil {
		t.Fatal(err)
	}
	comp, err := llm.Complete(context.Background(), CompletionRequest{
		System: "sys", JSON: true, MaxTokens: 50,
		Messages: []Message{{Role: "user", Text: "q1"}, {Role: "assistant", Text: "a1"}, {Role: "user", Text: "yardım"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if comp.Text != `{"capability":"help"}` || comp.Usage.InputTokens != 42 || comp.Usage.OutputTokens != 7 || comp.Usage.Provider != "openai-compatible" || comp.Usage.Model != "gpt-test" {
		t.Fatalf("completion = %+v", comp)
	}
	roles := make([]string, len(req.Messages))
	for i, m := range req.Messages {
		roles[i] = m.Role
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" || req.Model != "gpt-test" || len(req.ResponseFormat) == 0 {
		t.Fatalf("request roles %v model %s format %s", roles, req.Model, req.ResponseFormat)
	}
}

func TestLangChainServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"boom"}}`)
	}))
	t.Cleanup(srv.Close)
	llm, err := NewLangChainLLM(Profile{Provider: "groq", Model: "m", APIKey: "k", BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := llm.Complete(context.Background(), CompletionRequest{Messages: []Message{{Role: "user", Text: "x"}}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewLangChainLLMUnknownProvider(t *testing.T) {
	t.Parallel()
	if _, err := NewLangChainLLM(Profile{Provider: "fake"}); err == nil {
		t.Fatal("fake is not a langchain provider")
	}
}

func TestIntInfo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		m    map[string]any
		want int
	}{
		{nil, 0},
		{map[string]any{"PromptTokens": 5}, 5},
		{map[string]any{"InputTokens": int32(6)}, 6},
		{map[string]any{"prompt_tokens": int64(7)}, 7},
		{map[string]any{"input_tokens": 8.9}, 8},
		{map[string]any{"PromptTokens": "9"}, 0},
		{map[string]any{"PromptTokens": 1, "InputTokens": 2}, 1},
		{map[string]any{"PromptTokens": "x", "input_tokens": 3}, 3},
	}
	for _, c := range cases {
		if got := intInfo(c.m, "PromptTokens", "InputTokens", "prompt_tokens", "input_tokens"); got != c.want {
			t.Errorf("intInfo(%v) = %d, want %d", c.m, got, c.want)
		}
	}
}
