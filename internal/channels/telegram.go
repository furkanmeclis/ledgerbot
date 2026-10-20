package channels

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// TelegramConfig is a Telegram bot connection.
type TelegramConfig struct {
	BotToken    string `json:"bot_token"`
	Mode        string `json:"mode"`         // "webhook" or "poll" (default)
	SecretToken string `json:"secret_token"` // X-Telegram-Bot-Api-Secret-Token for webhooks
	APIURL      string `json:"api_url,omitempty"`
}

func (c TelegramConfig) api() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return "https://api.telegram.org"
}

// Telegram drives the Bot API in webhook or long-polling mode.
type Telegram struct{}

func (Telegram) Kind() domain.ChannelKind { return domain.ChannelTelegram }

type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int64 `json:"message_id"`
	Date      int64 `json:"date"`
	Chat      struct {
		ID   int64  `json:"id"`
		Type string `json:"type"`
	} `json:"chat"`
	Text     string   `json:"text"`
	Caption  string   `json:"caption"`
	Photo    []tgFile `json:"photo"`
	Voice    *tgFile  `json:"voice"`
	Audio    *tgFile  `json:"audio"`
	Document *tgFile  `json:"document"`
}

type tgFile struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
	FileSize int64  `json:"file_size"`
}

func (t Telegram) Webhook(_ http.ResponseWriter, r *http.Request, c Conn) ([]domain.InboundMessage, error) {
	var cfg TelegramConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return nil, err
	}
	if cfg.SecretToken != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")), []byte(cfg.SecretToken)) != 1 {
		return nil, ErrSignature
	}
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var u tgUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, nil
	}
	m, err := t.convert(r.Context(), cfg, c, u)
	if err != nil || m == nil {
		return nil, err
	}
	return []domain.InboundMessage{*m}, nil
}

func (t Telegram) convert(ctx context.Context, cfg TelegramConfig, c Conn, u tgUpdate) (*domain.InboundMessage, error) {
	m := u.Message
	if m == nil || m.Chat.Type != "private" {
		return nil, nil
	}
	msg := &domain.InboundMessage{
		ID:        "tg_" + strconv.FormatInt(m.Chat.ID, 10) + "_" + strconv.FormatInt(m.MessageID, 10),
		CompanyID: c.CompanyID, Channel: domain.ChannelTelegram, ChannelID: c.ID,
		Sender: strconv.FormatInt(m.Chat.ID, 10), ReceivedAt: time.Unix(m.Date, 0).UTC(),
	}
	var f *tgFile
	switch {
	case m.Text != "":
		msg.Kind, msg.Text = domain.KindText, m.Text
		return msg, nil
	case m.Voice != nil:
		msg.Kind, f = domain.KindAudio, m.Voice
	case m.Audio != nil:
		msg.Kind, f = domain.KindAudio, m.Audio
	case len(m.Photo) > 0:
		msg.Kind, f = domain.KindImage, &m.Photo[len(m.Photo)-1] // largest size
		if f.MimeType == "" {
			f.MimeType = "image/jpeg"
		}
	case m.Document != nil:
		f = m.Document
		msg.Kind = KindFromMIME(NormalizeMIME(f.MimeType))
	default:
		return nil, nil
	}
	if f.FileSize > MaxBody {
		return nil, nil
	}
	data, err := t.download(ctx, cfg, f.FileID)
	if err != nil {
		return nil, err
	}
	mime := NormalizeMIME(f.MimeType)
	if mime == "" {
		mime = "application/octet-stream"
	}
	msg.Text = m.Caption
	msg.Media = &domain.Media{MIME: mime, Filename: f.FileName, Data: data}
	return msg, nil
}

func (t Telegram) download(ctx context.Context, cfg TelegramConfig, fileID string) ([]byte, error) {
	var res struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := doJSON(ctx, http.MethodGet, cfg.api()+"/bot"+cfg.BotToken+"/getFile?file_id="+url.QueryEscape(fileID), nil, nil, &res); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.api()+"/file/bot"+cfg.BotToken+"/"+res.Result.FilePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram: file status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxBody))
}

func (Telegram) Send(ctx context.Context, c Conn, out domain.OutboundMessage) error {
	var cfg TelegramConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	chatID, err := strconv.ParseInt(out.To, 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: bad chat id %q", out.To)
	}
	return doJSON(ctx, http.MethodPost, cfg.api()+"/bot"+cfg.BotToken+"/sendMessage", nil,
		map[string]any{"chat_id": chatID, "text": out.Text}, nil)
}

func (Telegram) Status(ctx context.Context, c Conn) (string, error) {
	var cfg TelegramConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", err
	}
	if err := doJSON(ctx, http.MethodGet, cfg.api()+"/bot"+cfg.BotToken+"/getMe", nil, nil, nil); err != nil {
		return "error: invalid bot token", nil
	}
	return "connected", nil
}

// Poll long-polls getUpdates. Offsets are acknowledged only after sink
// accepted the message (enqueued), so a crash re-delivers instead of losing.
func (t Telegram) Poll(ctx context.Context, c Conn, sink func(context.Context, domain.InboundMessage) error) error {
	var cfg TelegramConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	if cfg.Mode == "webhook" {
		<-ctx.Done()
		return nil
	}
	// Webhook and getUpdates are mutually exclusive in Telegram.
	_ = doJSON(ctx, http.MethodPost, cfg.api()+"/bot"+cfg.BotToken+"/deleteWebhook", nil, map[string]any{}, nil)
	pollClient := &http.Client{Timeout: 70 * time.Second}
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		u := fmt.Sprintf("%s/bot%s/getUpdates?timeout=50&offset=%d&allowed_updates=%%5B%%22message%%22%%5D", cfg.api(), cfg.BotToken, offset)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		resp, err := pollClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		var res struct {
			OK     bool       `json:"ok"`
			Result []tgUpdate `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil || !res.OK {
			time.Sleep(backoff)
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second
		for _, upd := range res.Result {
			m, err := t.convert(ctx, cfg, c, upd)
			if err == nil && m != nil {
				if err := sink(ctx, *m); err != nil {
					break // retry this update on the next poll
				}
			}
			offset = upd.UpdateID + 1
		}
	}
	return nil
}
