package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// OpenAISTT calls an OpenAI-compatible /audio/transcriptions endpoint
// (OpenAI Whisper / gpt-4o-transcribe, Groq, faster-whisper-server, LocalAI).
type OpenAISTT struct {
	BaseURL, APIKey, Model, Provider string
	Client                           *http.Client
}

func (s *OpenAISTT) Transcribe(ctx context.Context, audio domain.Media, lang string) (Transcript, error) {
	base := strings.TrimRight(s.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	name := audio.Filename
	if name == "" {
		name = "audio" + audioExt(audio.MIME)
	}
	fw, err := w.CreateFormFile("file", name)
	if err != nil {
		return Transcript{}, err
	}
	if _, err := fw.Write(audio.Data); err != nil {
		return Transcript{}, err
	}
	_ = w.WriteField("model", s.Model)
	_ = w.WriteField("response_format", "verbose_json")
	if lang != "" {
		_ = w.WriteField("language", lang)
	}
	if err := w.Close(); err != nil {
		return Transcript{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/audio/transcriptions", &body)
	if err != nil {
		return Transcript{}, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	start := time.Now()
	resp, err := s.Client.Do(req)
	if err != nil {
		return Transcript{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Transcript{}, fmt.Errorf("ai: stt status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Text     string  `json:"text"`
		Language string  `json:"language"`
		Duration float64 `json:"duration"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Transcript{}, err
	}
	return Transcript{Text: out.Text, Language: out.Language, Usage: Usage{
		Provider: s.Provider, Model: s.Model, AudioSeconds: out.Duration, Latency: time.Since(start),
	}}, nil
}

func audioExt(mime string) string {
	switch {
	case strings.Contains(mime, "ogg"), strings.Contains(mime, "opus"):
		return ".ogg"
	case strings.Contains(mime, "mpeg"), strings.Contains(mime, "mp3"):
		return ".mp3"
	case strings.Contains(mime, "wav"):
		return ".wav"
	case strings.Contains(mime, "mp4"), strings.Contains(mime, "m4a"), strings.Contains(mime, "aac"):
		return ".m4a"
	case strings.Contains(mime, "webm"):
		return ".webm"
	}
	return ".ogg"
}

// OpenAIEmbedder calls an OpenAI-compatible /embeddings endpoint.
type OpenAIEmbedder struct {
	BaseURL, APIKey, Model, Provider string
	Dims                             int
	Client                           *http.Client
}

func (e *OpenAIEmbedder) Dimensions() int { return e.Dims }

func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, Usage, error) {
	base := strings.TrimRight(e.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	payload := map[string]any{"model": e.Model, "input": texts}
	if e.Dims > 0 {
		payload["dimensions"] = e.Dims
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	start := time.Now()
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, Usage{}, fmt.Errorf("ai: embeddings status %d: %s", resp.StatusCode, msg)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, Usage{}, err
	}
	vecs := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index >= 0 && d.Index < len(vecs) {
			vecs[d.Index] = d.Embedding
		}
	}
	return vecs, Usage{Provider: e.Provider, Model: e.Model, InputTokens: out.Usage.PromptTokens, Latency: time.Since(start)}, nil
}
