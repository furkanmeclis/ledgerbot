package channels

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// EmailConfig is a mailbox connection. Inbound mail arrives either through an
// inbound-parse webhook (Mailgun, Postmark, SES->Lambda, Cloudflare Email
// Workers all post JSON) or through IMAP polling (IMAP listener: follow-up).
type EmailConfig struct {
	Address       string `json:"address"`
	WebhookSecret string `json:"webhook_secret"`
	SMTPHost      string `json:"smtp_host"`
	SMTPPort      int    `json:"smtp_port"`
	SMTPUser      string `json:"smtp_user"`
	SMTPPass      string `json:"smtp_pass"`
	IMAPHost      string `json:"imap_host,omitempty"`
	IMAPUser      string `json:"imap_user,omitempty"`
	IMAPPass      string `json:"imap_pass,omitempty"`
}

// Email drives SMTP sending and JSON inbound webhooks.
type Email struct{}

func (Email) Kind() domain.ChannelKind { return domain.ChannelEmail }

// emailInbound is the normalized inbound JSON accepted on the webhook.
type emailInbound struct {
	MessageID   string `json:"message_id"`
	From        string `json:"from"`
	Subject     string `json:"subject"`
	Text        string `json:"text"`
	Attachments []struct {
		Filename    string `json:"filename"`
		ContentType string `json:"content_type"`
		Content     string `json:"content"` // base64
	} `json:"attachments"`
}

func (Email) Webhook(_ http.ResponseWriter, r *http.Request, c Conn) ([]domain.InboundMessage, error) {
	var cfg EmailConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return nil, err
	}
	if cfg.WebhookSecret == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Ledgerbot-Secret")), []byte(cfg.WebhookSecret)) != 1 {
		return nil, ErrSignature
	}
	raw, err := readBody(r)
	if err != nil {
		return nil, err
	}
	var in emailInbound
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, nil
	}
	addr, err := mail.ParseAddress(in.From)
	if err != nil {
		return nil, nil
	}
	base := domain.InboundMessage{
		CompanyID: c.CompanyID, Channel: domain.ChannelEmail, ChannelID: c.ID,
		Sender: strings.ToLower(addr.Address), ReceivedAt: time.Now().UTC(),
	}
	id := in.MessageID
	if id == "" {
		id = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	var out []domain.InboundMessage
	// Each attachment is its own document (invoices are usually attached);
	// the body becomes a text message only when there is no attachment.
	for i, a := range in.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.Content)
		if err != nil {
			continue
		}
		mt := NormalizeMIME(a.ContentType)
		if mt == "" {
			mt = mime.TypeByExtension(strings.ToLower(a.Filename[strings.LastIndexByte(a.Filename, '.')+1:]))
		}
		m := base
		m.ID = fmt.Sprintf("mail_%s_%d", id, i)
		m.Kind = KindFromMIME(mt)
		m.Text = in.Subject
		m.Media = &domain.Media{MIME: mt, Filename: a.Filename, Data: data}
		out = append(out, m)
	}
	if len(out) == 0 {
		m := base
		m.ID, m.Kind = "mail_"+id, domain.KindText
		m.Text = strings.TrimSpace(firstParagraph(in.Text))
		if m.Text == "" {
			m.Text = in.Subject
		}
		out = append(out, m)
	}
	return out, nil
}

// firstParagraph drops quoted replies and signatures.
func firstParagraph(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, ">") || t == "--" || strings.HasPrefix(t, "On ") && strings.HasSuffix(t, "wrote:") {
			break
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func (Email) Send(ctx context.Context, c Conn, out domain.OutboundMessage) error {
	var cfg EmailConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return err
	}
	if cfg.SMTPHost == "" {
		return errors.New("email: smtp not configured")
	}
	port := cfg.SMTPPort
	if port == 0 {
		port = 587
	}
	msg := "From: " + cfg.Address + "\r\nTo: " + out.To + "\r\nSubject: " + mime.QEncoding.Encode("utf-8", "Ledgerbot") +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + out.Text + "\r\n"
	var auth smtp.Auth
	if cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", cfg.SMTPUser, cfg.SMTPPass, cfg.SMTPHost)
	}
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(port)), auth, cfg.Address, []string{out.To}, []byte(msg))
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (Email) Status(_ context.Context, c Conn) (string, error) {
	var cfg EmailConfig
	if err := json.Unmarshal(c.Config, &cfg); err != nil {
		return "", err
	}
	if cfg.SMTPHost == "" {
		return "inbound_only", nil
	}
	return "configured", nil
}
