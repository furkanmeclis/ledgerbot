package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// WABAConfig is a Meta WhatsApp Cloud API connection.
type WABAConfig struct {
	PhoneNumberID string `json:"phone_number_id"`
	AccessToken   string `json:"access_token"`
	AppSecret     string `json:"app_secret"`   // X-Hub-Signature-256 verification
	VerifyToken   string `json:"verify_token"` // GET hub.verify_token
	APIVersion    string `json:"api_version,omitempty"`
	GraphURL      string `json:"graph_url,omitempty"` // override for tests
}

func (c WABAConfig) base() string {
	g := c.GraphURL
	if g == "" {
		g = "https://graph.facebook.com"
	}
	v := c.APIVersion
	if v == "" {
		v = "v21.0"
	}
	return strings.TrimRight(g, "/") + "/" + v
}

// WABA drives the Meta WhatsApp Cloud API.
type WABA struct{}

func (WABA) Kind() domain.ChannelKind { return domain.ChannelWABA }

type wabaPayload struct {
	Entry []struct {
		Changes []struct {
			Value struct {
				Messages []struct {
					From      string `json:"from"`
					ID        string `json:"id"`
					Timestamp string `json:"timestamp"`
					Type      string `json:"type"`
					Text      struct {
						Body string `json:"body"`
					} `json:"text"`
					Image    *wabaMedia `json:"image"`
					Audio    *wabaMedia `json:"audio"`
					Document *wabaMedia `json:"document"`
				} `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type wabaMedia struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
}

func (d WABA) Webhook(w http.ResponseWriter, r *http.Request, c Conn) ([]domain.InboundMessage, error) {
	var cfg WABAConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return nil, err
	}
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if q.Get("hub.mode") == "subscribe" && cfg.VerifyToken != "" && hmac.Equal([]byte(q.Get("hub.verify_token")), []byte(cfg.VerifyToken)) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, q.Get("hub.challenge"))
			return nil, ErrHandled
		}
		return nil, ErrSignature
	}
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	if cfg.AppSecret != "" {
		sig, err := hex.DecodeString(strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), "sha256="))
		mac := hmac.New(sha256.New, []byte(cfg.AppSecret))
		mac.Write(raw)
		if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
			return nil, ErrSignature
		}
	}
	var p wabaPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil
	}
	var out []domain.InboundMessage
	for _, e := range p.Entry {
		for _, ch := range e.Changes {
			for _, m := range ch.Value.Messages {
				msg := domain.InboundMessage{
					ID: "waba_" + m.ID, CompanyID: c.CompanyID, Channel: domain.ChannelWABA, ChannelID: c.ID,
					Sender: "+" + strings.TrimPrefix(m.From, "+"), ReceivedAt: time.Now().UTC(),
				}
				if sec, err := strconv.ParseInt(m.Timestamp, 10, 64); err == nil {
					msg.ReceivedAt = time.Unix(sec, 0).UTC()
				}
				var media *wabaMedia
				switch m.Type {
				case "text":
					msg.Kind, msg.Text = domain.KindText, m.Text.Body
				case "image":
					msg.Kind, media = domain.KindImage, m.Image
				case "audio":
					msg.Kind, media = domain.KindAudio, m.Audio
				case "document":
					msg.Kind, media = domain.KindDocument, m.Document
				default:
					continue
				}
				if media != nil {
					data, err := d.download(r.Context(), cfg, media.ID)
					if err != nil {
						return nil, fmt.Errorf("waba: media %s: %w", media.ID, err)
					}
					msg.Text = media.Caption
					msg.Media = &domain.Media{MIME: NormalizeMIME(media.MimeType), Filename: media.Filename, Data: data}
				}
				out = append(out, msg)
			}
		}
	}
	return out, nil
}

func (WABA) download(ctx context.Context, cfg WABAConfig, id string) ([]byte, error) {
	auth := map[string]string{"Authorization": "Bearer " + cfg.AccessToken}
	var meta struct {
		URL string `json:"url"`
	}
	if err := doJSON(ctx, http.MethodGet, cfg.base()+"/"+id, auth, nil, &meta); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AccessToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxBody))
}

func (WABA) Send(ctx context.Context, c Conn, out domain.OutboundMessage) error {
	var cfg WABAConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	return doJSON(ctx, http.MethodPost, cfg.base()+"/"+cfg.PhoneNumberID+"/messages",
		map[string]string{"Authorization": "Bearer " + cfg.AccessToken},
		map[string]any{
			"messaging_product": "whatsapp", "recipient_type": "individual",
			"to": strings.TrimPrefix(out.To, "+"), "type": "text",
			"text": map[string]any{"body": out.Text, "preview_url": false},
		}, nil)
}

func (WABA) Status(ctx context.Context, c Conn) (string, error) {
	var cfg WABAConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", err
	}
	var info struct {
		QualityRating string `json:"quality_rating"`
	}
	if err := doJSON(ctx, http.MethodGet, cfg.base()+"/"+cfg.PhoneNumberID+"?fields=quality_rating",
		map[string]string{"Authorization": "Bearer " + cfg.AccessToken}, nil, &info); err != nil {
		return "error: " + err.Error(), nil
	}
	return "connected", nil
}
