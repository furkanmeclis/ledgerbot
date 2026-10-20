package channels

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// WuzapiConfig is the connection config for a WuzAPI user (per company or central).
type WuzapiConfig struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`    // WuzAPI user token
	HMACKey string `json:"hmac_key"` // webhook signature key
}

// Wuzapi drives WuzAPI (whatsmeow) servers.
type Wuzapi struct{}

func (Wuzapi) Kind() domain.ChannelKind { return domain.ChannelWuzapi }

type wuzapiEvent struct {
	Type  string `json:"type"`
	Event struct {
		Info struct {
			Sender    string `json:"Sender"`
			SenderAlt string `json:"SenderAlt"`
			IsFromMe  bool   `json:"IsFromMe"`
			IsGroup   bool   `json:"IsGroup"`
			ID        string `json:"ID"`
			PushName  string `json:"PushName"`
			Timestamp string `json:"Timestamp"`
		} `json:"Info"`
		Message map[string]json.RawMessage `json:"Message"`
	} `json:"event"`
	Base64   string `json:"base64"`
	MimeType string `json:"mimeType"`
	FileName string `json:"fileName"`
}

type wuzapiMedia struct {
	Mimetype string `json:"mimetype"`
	Caption  string `json:"caption"`
	FileName string `json:"fileName"`
}

var phoneJID = regexp.MustCompile(`^(\d+)(?:[.:]\d+)*@s\.whatsapp\.net$`)

// PhoneFromJID converts "905551112233:12@s.whatsapp.net" to "+905551112233".
func PhoneFromJID(jid string) string {
	m := phoneJID.FindStringSubmatch(jid)
	if m == nil {
		return ""
	}
	return "+" + m[1]
}

func (Wuzapi) Webhook(_ http.ResponseWriter, r *http.Request, c Conn) ([]domain.InboundMessage, error) {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return nil, err
	}
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	if cfg.HMACKey != "" {
		sig, err := hex.DecodeString(r.Header.Get("X-Hmac-Signature"))
		mac := hmac.New(sha256.New, []byte(cfg.HMACKey))
		mac.Write(raw)
		if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
			return nil, ErrSignature
		}
	}
	// WuzAPI posts JSON, or form-encoded with the JSON in "jsonData".
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
		if v, err := url.ParseQuery(string(raw)); err == nil && v.Get("jsonData") != "" {
			raw = []byte(v.Get("jsonData"))
		}
	}
	var ev wuzapiEvent
	if err := json.Unmarshal(raw, &ev); err != nil || ev.Type != "Message" {
		return nil, nil
	}
	info := ev.Event.Info
	if info.IsFromMe || info.IsGroup || info.ID == "" {
		return nil, nil
	}
	from := PhoneFromJID(info.Sender)
	if from == "" {
		from = PhoneFromJID(info.SenderAlt)
	}
	if from == "" {
		return nil, nil
	}
	msg := domain.InboundMessage{
		ID: "wa_" + info.ID, CompanyID: c.CompanyID, Channel: domain.ChannelWuzapi, ChannelID: c.ID,
		Sender: from, ReceivedAt: time.Now().UTC(),
	}
	if ts, err := time.Parse(time.RFC3339, info.Timestamp); err == nil {
		msg.ReceivedAt = ts
	}
	m := ev.Event.Message
	var text string
	if v, ok := m["conversation"]; ok && json.Unmarshal(v, &text) == nil {
		msg.Kind, msg.Text = domain.KindText, text
		return []domain.InboundMessage{msg}, nil
	}
	if v, ok := m["extendedTextMessage"]; ok {
		var ext struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(v, &ext) == nil && ext.Text != "" {
			msg.Kind, msg.Text = domain.KindText, ext.Text
			return []domain.InboundMessage{msg}, nil
		}
	}
	for _, k := range []struct {
		key  string
		kind domain.MessageKind
	}{{"imageMessage", domain.KindImage}, {"documentMessage", domain.KindDocument}, {"audioMessage", domain.KindAudio}} {
		v, ok := m[k.key]
		if !ok {
			continue
		}
		var part wuzapiMedia
		_ = json.Unmarshal(v, &part)
		if ev.Base64 == "" {
			return nil, nil
		}
		data, err := base64.StdEncoding.DecodeString(stripDataURL(ev.Base64))
		if err != nil {
			return nil, nil
		}
		mime := NormalizeMIME(part.Mimetype)
		if mime == "" {
			mime = NormalizeMIME(ev.MimeType)
		}
		if mime == "" {
			mime = "application/octet-stream"
		}
		name := part.FileName
		if name == "" {
			name = ev.FileName
		}
		msg.Kind, msg.Text = k.kind, part.Caption
		msg.Media = &domain.Media{MIME: mime, Filename: name, Data: data}
		return []domain.InboundMessage{msg}, nil
	}
	return nil, nil
}

func stripDataURL(s string) string {
	if i := strings.Index(s, ";base64,"); i >= 0 && strings.HasPrefix(s, "data:") {
		return s[i+8:]
	}
	return s
}

type wuzapiResp struct {
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

func (w Wuzapi) call(ctx context.Context, cfg WuzapiConfig, method, path string, body any) (json.RawMessage, error) {
	var out wuzapiResp
	err := doJSON(ctx, method, strings.TrimRight(cfg.BaseURL, "/")+path, map[string]string{"Token": cfg.Token}, body, &out)
	if err != nil {
		return nil, err
	}
	if !out.Success && out.Error != "" {
		return nil, &url.Error{Op: method, URL: path, Err: errString(out.Error)}
	}
	return out.Data, nil
}

type errString string

func (e errString) Error() string { return string(e) }

func (w Wuzapi) Send(ctx context.Context, c Conn, out domain.OutboundMessage) error {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	_, err := w.call(ctx, cfg, http.MethodPost, "/chat/send/text", map[string]string{
		"Phone": strings.TrimPrefix(out.To, "+"), "Body": out.Text,
	})
	return err
}

func (w Wuzapi) Status(ctx context.Context, c Conn) (string, error) {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", err
	}
	data, err := w.call(ctx, cfg, http.MethodGet, "/session/status", nil)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	var st struct {
		Connected bool `json:"connected"`
		LoggedIn  bool `json:"loggedIn"`
	}
	_ = json.Unmarshal(data, &st)
	switch {
	case st.Connected && st.LoggedIn:
		return "connected", nil
	case st.Connected:
		return "waiting_qr", nil
	default:
		return "disconnected", nil
	}
}

// Connect starts the WhatsApp session so a QR code becomes available.
func (w Wuzapi) Connect(ctx context.Context, c Conn) error {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	_, err := w.call(ctx, cfg, http.MethodPost, "/session/connect", map[string]any{"Subscribe": []string{"Message"}, "Immediate": true})
	return err
}

// QR returns a data:image/png QR code, or "" when already logged in.
func (w Wuzapi) QR(ctx context.Context, c Conn) (string, error) {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", err
	}
	data, err := w.call(ctx, cfg, http.MethodGet, "/session/qr", nil)
	if err != nil {
		return "", err
	}
	var qr struct {
		QRCode string `json:"QRCode"`
	}
	_ = json.Unmarshal(data, &qr)
	return qr.QRCode, nil
}

// Logout ends the session; a new QR is needed afterwards.
func (w Wuzapi) Logout(ctx context.Context, c Conn) error {
	var cfg WuzapiConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	_, err := w.call(ctx, cfg, http.MethodPost, "/session/logout", nil)
	return err
}
