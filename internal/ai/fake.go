package ai

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// FakeReceipt is what FakeLLM returns for an image it cannot interpret.
const FakeReceipt = `{"vendor":"Örnek Kırtasiye Ltd. Şti.","vendor_tax_id":"1234567890","document_no":"A-000123","date":"","currency":"TRY",
"lines":[{"name":"A4 Fotokopi Kağıdı","quantity":2,"unit":"paket","unit_price":"185,00","vat_rate":20},
{"name":"Toner 85A","quantity":1,"unit":"adet","unit_price":"1.450,00","vat_rate":20}],
"total":"2.184,00","confidence":0.93}`

// FakeLLM is deterministic and offline. For multimodal extraction it echoes
// image bytes that are already JSON (so tests control the result) or returns
// FakeReceipt; for text it reports that it did not understand, which makes the
// rule-based parser the only source of intents in fake mode.
type FakeLLM struct{}

func (FakeLLM) Complete(_ context.Context, req CompletionRequest) (Completion, error) {
	u := Usage{Provider: "fake", Model: "fake", InputTokens: approxTokens(req), OutputTokens: 32}
	if len(req.Images) > 0 {
		if json.Valid(req.Images[0].Data) {
			return Completion{Text: string(req.Images[0].Data), Usage: u}, nil
		}
		return Completion{Text: FakeReceipt, Usage: u}, nil
	}
	if req.JSON {
		return Completion{Text: `{"capability":"unknown","args":{},"language":"tr"}`, Usage: u}, nil
	}
	return Completion{Text: "Anlayamadım.", Usage: u}, nil
}

func approxTokens(req CompletionRequest) int {
	n := utf8.RuneCountInString(req.System)
	for _, m := range req.Messages {
		n += utf8.RuneCountInString(m.Text)
	}
	return n/4 + 1
}

// FakeSTT treats the audio bytes as the transcript, so the dev channel can send
// "voice notes" as plain text.
type FakeSTT struct{}

func (FakeSTT) Transcribe(_ context.Context, a domain.Media, lang string) (Transcript, error) {
	if lang == "" {
		lang = "tr"
	}
	return Transcript{Text: strings.TrimSpace(string(a.Data)), Language: lang, Usage: Usage{
		Provider: "fake", Model: "fake", AudioSeconds: float64(len(a.Data)) / 16,
	}}, nil
}

// FakeEmbedder hashes words into a fixed-size, L2-normalized vector. Similar
// texts get similar vectors, which is enough for RAG tests.
type FakeEmbedder struct{ Dims int }

func (f FakeEmbedder) Dimensions() int { return f.Dims }

func (f FakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, Usage, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, f.Dims)
		for _, w := range strings.Fields(strings.ToLower(t)) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[int(h.Sum32())%len(v)]++
		}
		var norm float64
		for _, x := range v {
			norm += float64(x * x)
		}
		if norm > 0 {
			inv := float32(1 / math.Sqrt(norm))
			for j := range v {
				v[j] *= inv
			}
		}
		out[i] = v
	}
	return out, Usage{Provider: "fake", Model: "fake"}, nil
}
