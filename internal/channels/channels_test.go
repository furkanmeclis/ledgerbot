package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/store"
)

func conn(kind domain.ChannelKind, cfg any) Conn {
	b, _ := json.Marshal(cfg)
	return Conn{Connection: store.Connection{ID: "ch_1", CompanyID: "demo", Kind: kind}, Config: b}
}

func hmacHex(key, body string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func TestWuzapiWebhook(t *testing.T) {
	c := conn(domain.ChannelWuzapi, WuzapiConfig{HMACKey: "k"})
	body := `{"type":"Message","event":{"Info":{"Sender":"905321112233:12@s.whatsapp.net","IsFromMe":false,"IsGroup":false,"ID":"ABC","Timestamp":"2026-10-20T10:00:00Z"},"Message":{"conversation":"stok toner"}}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Hmac-Signature", hmacHex("k", body))
	msgs, err := Wuzapi{}.Webhook(httptest.NewRecorder(), req, c)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs=%v err=%v", msgs, err)
	}
	if m := msgs[0]; m.Sender != "+905321112233" || m.Text != "stok toner" || m.CompanyID != "demo" {
		t.Fatalf("unexpected %+v", m)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Hmac-Signature", hmacHex("wrong", body))
	if _, err := (Wuzapi{}).Webhook(httptest.NewRecorder(), req, c); !errors.Is(err, ErrSignature) {
		t.Fatalf("expected signature error, got %v", err)
	}

	media := `{"type":"Message","event":{"Info":{"Sender":"905321112233@s.whatsapp.net","ID":"IMG","Timestamp":"x"},"Message":{"audioMessage":{"mimetype":"audio/ogg; codecs=opus"}}},"base64":"` +
		base64.StdEncoding.EncodeToString([]byte("sesli")) + `"}`
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(media))
	req.Header.Set("X-Hmac-Signature", hmacHex("k", media))
	msgs, err = Wuzapi{}.Webhook(httptest.NewRecorder(), req, c)
	if err != nil || len(msgs) != 1 || msgs[0].Kind != domain.KindAudio || msgs[0].Media.MIME != "audio/ogg" || string(msgs[0].Media.Data) != "sesli" {
		t.Fatalf("media parse: %+v %v", msgs, err)
	}
}

func TestWABAWebhook(t *testing.T) {
	c := conn(domain.ChannelWABA, WABAConfig{AppSecret: "s", VerifyToken: "v"})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?hub.mode=subscribe&hub.verify_token=v&hub.challenge=42", nil)
	if _, err := (WABA{}).Webhook(rec, req, c); !errors.Is(err, ErrHandled) || rec.Body.String() != "42" {
		t.Fatalf("challenge: %v %q", err, rec.Body.String())
	}
	body := `{"entry":[{"changes":[{"value":{"messages":[{"from":"905321112233","id":"wamid.1","timestamp":"1760000000","type":"text","text":{"body":"yardım"}}]}}]}]}`
	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+hmacHex("s", body))
	msgs, err := WABA{}.Webhook(httptest.NewRecorder(), req, c)
	if err != nil || len(msgs) != 1 || msgs[0].Sender != "+905321112233" || msgs[0].Text != "yardım" {
		t.Fatalf("waba parse: %+v %v", msgs, err)
	}
}

func TestTelegramWebhookAndSend(t *testing.T) {
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()
	c := conn(domain.ChannelTelegram, TelegramConfig{BotToken: "T", SecretToken: "sec", APIURL: srv.URL})
	body := `{"update_id":1,"message":{"message_id":7,"date":1760000000,"chat":{"id":100000002,"type":"private"},"text":"gün sonu özeti"}}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "sec")
	msgs, err := Telegram{}.Webhook(httptest.NewRecorder(), req, c)
	if err != nil || len(msgs) != 1 || msgs[0].Sender != "100000002" {
		t.Fatalf("telegram parse: %+v %v", msgs, err)
	}
	if err := (Telegram{}).Send(t.Context(), c, domain.OutboundMessage{To: "100000002", Text: "ok"}); err != nil {
		t.Fatal(err)
	}
	if sent["text"] != "ok" {
		t.Fatalf("sent %v", sent)
	}
}

func TestEmailWebhook(t *testing.T) {
	c := conn(domain.ChannelEmail, EmailConfig{WebhookSecret: "x"})
	body := `{"message_id":"m1","from":"Ayşe <MUHASEBE@demo.local>","subject":"fatura","text":"ekte","attachments":[{"filename":"f.xml","content_type":"application/xml","content":"` +
		base64.StdEncoding.EncodeToString([]byte("<Invoice/>")) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("X-Ledgerbot-Secret", "x")
	msgs, err := Email{}.Webhook(httptest.NewRecorder(), req, c)
	if err != nil || len(msgs) != 1 || msgs[0].Sender != "muhasebe@demo.local" || msgs[0].Kind != domain.KindDocument {
		t.Fatalf("email parse: %+v %v", msgs, err)
	}
}

func TestPhoneFromJID(t *testing.T) {
	for in, want := range map[string]string{"905321112233@s.whatsapp.net": "+905321112233", "905321112233:5@s.whatsapp.net": "+905321112233", "123@lid": ""} {
		if got := PhoneFromJID(in); got != want {
			t.Errorf("%s -> %s want %s", in, got, want)
		}
	}
}
