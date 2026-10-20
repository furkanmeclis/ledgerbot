package assistant

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
	"github.com/furkanmeclis/ledgerbot/internal/sign"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

func TestPluginCapabilities(t *testing.T) {
	var got pluginRequest
	var verified bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		verified = sign.Verify("plug", r.Header.Get(sign.Header), body, time.Now(), time.Minute)
		_ = json.Unmarshal(body, &got)
		switch got.Capability {
		case "x_cargo":
			_, _ = w.Write([]byte(`{"text":"Kargo ` + got.Args["code"] + ` yolda, yarın teslim."}`))
		case "x_visit":
			_, _ = w.Write([]byte(`{"summary":"Ziyaret kaydı: ` + got.Args["who"] + `","record":{"kind":"custom","note":"visit"}}`))
		}
	}))
	defer srv.Close()

	h := newHarness(t, func(s *tenant.Settings, m *tenant.Matrix) {
		s.SaveMode = domain.SaveAuto
		s.Plugins = []tenant.Plugin{
			{Name: "x_cargo", Description: "Track a shipment", Args: []string{"code"}, URL: srv.URL, Secret: "plug",
				Patterns: []string{`(?i)^kargo\s+takip\s+(?P<code>[A-Z0-9]+)$`}},
			{Name: "x_visit", Description: "Log a customer visit", Args: []string{"who"}, URL: srv.URL, Secret: "plug", Write: true,
				Patterns: []string{`(?i)^ziyaret\s+(?P<who>.+)$`}},
		}
	})
	r := h.send(t, "owner", "kargo takip TR123")
	if joined(r) != "Kargo TR123 yolda, yarın teslim." || !verified || got.Actor.Role != "owner" {
		t.Fatalf("read plugin: %q verified=%v req=%+v", joined(r), verified, got)
	}
	r = h.send(t, "owner", "ziyaret Ahmet Yılmaz")
	if len(r.Drafts) != 1 || r.Drafts[0].Status != domain.DraftCommitted || !strings.Contains(joined(r), "Ziyaret kaydı: Ahmet Yılmaz") {
		t.Fatalf("write plugin: %+v %q", r.Drafts, joined(r))
	}
	// Plugins obey the permission matrix: sales may only use query_* etc.
	if r := h.send(t, "sales", "kargo takip TR123"); !strings.Contains(joined(r), "yetkiniz yok") {
		t.Fatalf("plugin permission: %q", joined(r))
	}
}
