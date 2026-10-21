package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/dataprovider"
	"github.com/furkanmeclis/ledgerbot/internal/nlu"
	"github.com/furkanmeclis/ledgerbot/internal/sign"
	"github.com/furkanmeclis/ledgerbot/internal/tenant"
)

// Plugin capabilities are declared per company in settings.plugins and served
// by the host (or anyone) over HTTPS. ledgerbot signs every call, enforces the
// permission matrix and the save mode exactly like built-ins, and sends the
// plugin's text verbatim.
//
// Request:  POST url  {company_id, capability, actor, args, text, language}
// Response: {"text": "...", "summary": "...", "record": {...}}   (record only for write plugins)

var pluginClient = &http.Client{Timeout: 10 * time.Second}

type pluginRequest struct {
	CompanyID  string            `json:"company_id"`
	Capability string            `json:"capability"`
	Actor      pluginActor       `json:"actor"`
	Args       map[string]string `json:"args"`
	Text       string            `json:"text"`
	Language   string            `json:"language"`
}

type pluginActor struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Name string `json:"name,omitempty"`
}

type pluginResponse struct {
	Text    string               `json:"text"`
	Summary string               `json:"summary"`
	Record  *dataprovider.Record `json:"record"`
}

// pluginCapability turns a plugin declaration into a capability.
func pluginCapability(p tenant.Plugin) Capability {
	return Capability{
		Name: p.Name, Description: p.Description, Args: p.Args, Write: p.Write,
		Run: func(ctx context.Context, e *Engine, c *Call) (Outcome, error) {
			return callPlugin(ctx, p, c)
		},
	}
}

func callPlugin(ctx context.Context, p tenant.Plugin, c *Call) (Outcome, error) {
	body, err := json.Marshal(pluginRequest{
		CompanyID: string(c.Actor.CompanyID), Capability: p.Name,
		Actor: pluginActor{ID: string(c.Actor.UserID), Role: c.Actor.Role, Name: c.Actor.Name},
		Args:  c.Args, Text: c.Msg.Text, Language: c.Lang,
	})
	if err != nil {
		return Outcome{}, err
	}
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return Outcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ledgerbot-plugins/1")
	req.Header.Set("X-Ledgerbot-Capability", p.Name)
	if p.Secret != "" {
		req.Header.Set(sign.Header, sign.Make(p.Secret, time.Now().Unix(), body))
	}
	resp, err := pluginClient.Do(req)
	if err != nil {
		return Outcome{}, fmt.Errorf("plugin %s: %w", p.Name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Outcome{}, err
	}
	if resp.StatusCode >= 300 {
		return Outcome{}, fmt.Errorf("plugin %s: status %d", p.Name, resp.StatusCode)
	}
	var out pluginResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Outcome{}, fmt.Errorf("plugin %s: bad response: %w", p.Name, err)
	}
	o := Outcome{Text: strings.TrimSpace(out.Text)}
	if p.Write && out.Record != nil {
		rec := *out.Record
		if rec.Kind == "" {
			rec.Kind = dataprovider.RecordCustom
		}
		rec.Date = time.Now()
		o.Record, o.Summary = &rec, out.Summary
		if o.Summary == "" {
			o.Summary = o.Text
			o.Text = ""
		}
	}
	return o, nil
}

// capability resolves a name: built-ins first, then the company's plugins.
func (e *Engine) capability(snap *tenant.Snapshot, name string) (Capability, bool) {
	if c, ok := e.Caps.Get(name); ok {
		return c, true
	}
	for _, p := range snap.Settings.Plugins {
		if p.Name == name && p.URL != "" {
			return pluginCapability(p), true
		}
	}
	return Capability{}, false
}

// docs returns capability docs including the company's plugins (for the LLM).
func (e *Engine) docs(snap *tenant.Snapshot) []nlu.CapabilityDoc {
	d := e.Caps.Docs()
	for _, p := range snap.Settings.Plugins {
		if _, builtin := e.Caps.Get(p.Name); !builtin {
			d = append(d, nlu.CapabilityDoc{Name: p.Name, Description: p.Description, Args: p.Args})
		}
	}
	return d
}

var patternCache sync.Map // pattern -> *regexp.Regexp (nil when invalid)

// matchPlugins tries plugin patterns (named groups become args). It runs after
// the built-in rules and before the LLM, so plugins work without an AI provider.
func matchPlugins(snap *tenant.Snapshot, text string) (nlu.Intent, bool) {
	for _, p := range snap.Settings.Plugins {
		for _, pat := range p.Patterns {
			v, ok := patternCache.Load(pat)
			if !ok {
				re, err := regexp.Compile(pat)
				if err != nil {
					re = nil
				}
				v, _ = patternCache.LoadOrStore(pat, re)
			}
			re, _ := v.(*regexp.Regexp)
			if re == nil {
				continue
			}
			m := re.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			in := nlu.Intent{Capability: p.Name, Args: map[string]string{}, Source: "plugin"}
			for i, name := range re.SubexpNames() {
				if i > 0 && name != "" {
					in.Args[name] = strings.TrimSpace(m[i])
				}
			}
			return in, true
		}
	}
	return nlu.Intent{}, false
}
