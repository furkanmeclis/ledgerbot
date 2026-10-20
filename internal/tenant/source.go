package tenant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/furkanmeclis/ledgerbot/internal/domain"
)

// HTTPSource reads the contracts from the host system:
//
//	GET {base}/companies/{id}/settings     -> Settings
//	GET {base}/companies/{id}/permissions  -> Matrix
//
// Requests carry "Authorization: Bearer {token}".
type HTTPSource struct {
	Base   string
	Token  string
	Client *http.Client
}

func (h *HTTPSource) Settings(ctx context.Context, c domain.CompanyID) (Settings, error) {
	var s Settings
	err := h.get(ctx, c, "settings", &s)
	return s, err
}

func (h *HTTPSource) Matrix(ctx context.Context, c domain.CompanyID) (Matrix, error) {
	var m Matrix
	err := h.get(ctx, c, "permissions", &m)
	return m, err
}

func (h *HTTPSource) get(ctx context.Context, c domain.CompanyID, what string, out any) error {
	u := h.Base + "/companies/" + url.PathEscape(string(c)) + "/" + what
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	req.Header.Set("Accept", "application/json")
	cl := h.Client
	if cl == nil {
		cl = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrUnknownCompany
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("tenant: %s %s: status %d", what, c, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// FileSource reads seed JSON from a directory: {dir}/{company}.json containing
// {"settings": {...}, "permissions": {...}}. Used for development and demos.
type FileSource struct{ Dir string }

type seedFile struct {
	Settings    Settings `json:"settings"`
	Permissions Matrix   `json:"permissions"`
}

func (f *FileSource) load(c domain.CompanyID) (seedFile, error) {
	var sf seedFile
	b, err := os.ReadFile(filepath.Join(f.Dir, filepath.Base(string(c))+".json"))
	if os.IsNotExist(err) {
		return sf, ErrUnknownCompany
	}
	if err != nil {
		return sf, err
	}
	err = json.Unmarshal(b, &sf)
	sf.Settings.CompanyID, sf.Permissions.CompanyID = c, c
	return sf, err
}

func (f *FileSource) Settings(_ context.Context, c domain.CompanyID) (Settings, error) {
	sf, err := f.load(c)
	return sf.Settings, err
}

func (f *FileSource) Matrix(_ context.Context, c domain.CompanyID) (Matrix, error) {
	sf, err := f.load(c)
	return sf.Permissions, err
}

// Companies lists company ids available in the seed directory.
func (f *FileSource) Companies() ([]domain.CompanyID, error) {
	m, err := filepath.Glob(filepath.Join(f.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	out := make([]domain.CompanyID, 0, len(m))
	for _, p := range m {
		out = append(out, domain.CompanyID(filepath.Base(p[:len(p)-len(".json")])))
	}
	return out, nil
}

// Lookup implements Directory: GET {base}/identities?channel=..&address=..
// -> {"companies": ["..."]}.
func (h *HTTPSource) Lookup(ctx context.Context, ch domain.ChannelKind, address string) ([]domain.CompanyID, error) {
	u := h.Base + "/identities?channel=" + url.QueryEscape(string(ch)) + "&address=" + url.QueryEscape(address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if h.Token != "" {
		req.Header.Set("Authorization", "Bearer "+h.Token)
	}
	cl := h.Client
	if cl == nil {
		cl = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tenant: identities: status %d", resp.StatusCode)
	}
	var out struct {
		Companies []domain.CompanyID `json:"companies"`
	}
	err = json.NewDecoder(resp.Body).Decode(&out)
	return out.Companies, err
}

// Lookup implements Directory by scanning seed files.
func (f *FileSource) Lookup(ctx context.Context, ch domain.ChannelKind, address string) ([]domain.CompanyID, error) {
	ids, err := f.Companies()
	if err != nil {
		return nil, err
	}
	var out []domain.CompanyID
	for _, id := range ids {
		m, err := f.Matrix(ctx, id)
		if err != nil {
			continue
		}
		snap := NewSnapshot(Settings{CompanyID: id}, m)
		if _, ok := snap.ResolveActor(ch, address); ok {
			out = append(out, id)
		}
	}
	return out, nil
}
