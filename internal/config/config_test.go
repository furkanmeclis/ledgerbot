package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

var allKeys = []string{
	"LEDGERBOT_DATABASE_URL", "LEDGERBOT_DB_MAX_CONNS", "LEDGERBOT_HTTP_ADDR", "LEDGERBOT_PUBLIC_URL",
	"LEDGERBOT_TENANT_SOURCE", "LEDGERBOT_SEED_DIR", "LEDGERBOT_HOST_API_URL", "LEDGERBOT_HOST_API_TOKEN",
	"LEDGERBOT_TENANT_TTL", "LEDGERBOT_DATA_DRIVER", "LEDGERBOT_PROVIDERS_FILE", "LEDGERBOT_FX_SOURCE",
	"LEDGERBOT_SECRET_KEY", "LEDGERBOT_ADMIN_TOKEN", "LEDGERBOT_META_APP_SECRET", "LEDGERBOT_DEV",
	"LEDGERBOT_WORKER_CONCURRENCY", "LEDGERBOT_PER_COMPANY_INFLIGHT", "LEDGERBOT_SIMULATE_EVERY",
	"LEDGERBOT_MAX_LISTENERS", "LEDGERBOT_RATE_RPS", "LEDGERBOT_RATE_BURST", "LEDGERBOT_LOG_LEVEL",
}

// clearEnv blanks every key so the host environment cannot leak into a test;
// an empty value is treated as unset by env().
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allKeys {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("LEDGERBOT_DEV", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL:        "postgres://ledgerbot:ledgerbot@localhost:5432/ledgerbot?sslmode=disable",
		DBMaxConns:         40,
		HTTPAddr:           ":8080",
		PublicURL:          "http://localhost:8080",
		TenantSource:       "file",
		SeedDir:            "seed/companies",
		TenantTTL:          30 * time.Second,
		DataDriver:         "fake",
		FXSource:           "tcmb",
		Dev:                true,
		WorkerConcurrency:  64,
		PerCompanyInflight: 4,
		MaxListeners:       1000,
		RateRPS:            20,
		RateBurst:          40,
		LogLevel:           "info",
	}
	if c != want {
		t.Fatalf("defaults:\n got %+v\nwant %+v", c, want)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	set := map[string]string{
		"LEDGERBOT_DATABASE_URL":         "postgres://x@db/y",
		"LEDGERBOT_DB_MAX_CONNS":         "12",
		"LEDGERBOT_HTTP_ADDR":            ":9090",
		"LEDGERBOT_PUBLIC_URL":           "https://bot.example.com",
		"LEDGERBOT_TENANT_SOURCE":        "http",
		"LEDGERBOT_HOST_API_URL":         "https://host.example.com/api///",
		"LEDGERBOT_HOST_API_TOKEN":       "tok",
		"LEDGERBOT_TENANT_TTL":           "2m",
		"LEDGERBOT_DATA_DRIVER":          "api",
		"LEDGERBOT_PROVIDERS_FILE":       "providers.json",
		"LEDGERBOT_FX_SOURCE":            "static",
		"LEDGERBOT_SECRET_KEY":           strings.Repeat("ab", 32),
		"LEDGERBOT_ADMIN_TOKEN":          "adm",
		"LEDGERBOT_META_APP_SECRET":      "meta",
		"LEDGERBOT_DEV":                  "0",
		"LEDGERBOT_WORKER_CONCURRENCY":   "8",
		"LEDGERBOT_PER_COMPANY_INFLIGHT": "2",
		"LEDGERBOT_SIMULATE_EVERY":       "1500ms",
		"LEDGERBOT_MAX_LISTENERS":        "5",
		"LEDGERBOT_RATE_RPS":             "2.5",
		"LEDGERBOT_RATE_BURST":           "7",
		"LEDGERBOT_LOG_LEVEL":            "debug",
	}
	for k, v := range set {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		DatabaseURL: "postgres://x@db/y", DBMaxConns: 12, HTTPAddr: ":9090", PublicURL: "https://bot.example.com",
		TenantSource: "http", SeedDir: "seed/companies", HostAPIURL: "https://host.example.com/api", HostAPIToken: "tok",
		TenantTTL: 2 * time.Minute, DataDriver: "api", ProvidersFile: "providers.json", FXSource: "static",
		SecretKey: strings.Repeat("ab", 32), AdminToken: "adm", MetaAppSecret: "meta", Dev: false,
		WorkerConcurrency: 8, PerCompanyInflight: 2, SimulateEvery: 1500 * time.Millisecond, MaxListeners: 5,
		RateRPS: 2.5, RateBurst: 7, LogLevel: "debug",
	}
	if c != want {
		t.Fatalf("overrides:\n got %+v\nwant %+v", c, want)
	}
}

func TestLoadDBMaxConnsClamp(t *testing.T) {
	for in, want := range map[string]int32{"0": 1, "-5": 1, "1": 1, "1000": 1000, "5000": 1000, "abc": 40, "99999999999999999999": 40} {
		t.Run(in, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("LEDGERBOT_DEV", "1")
			t.Setenv("LEDGERBOT_DB_MAX_CONNS", in)
			c, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if c.DBMaxConns != want {
				t.Fatalf("DBMaxConns = %d, want %d", c.DBMaxConns, want)
			}
		})
	}
}

func TestLoadInvalidValuesFallBack(t *testing.T) {
	clearEnv(t)
	t.Setenv("LEDGERBOT_DEV", "yes please")
	t.Setenv("LEDGERBOT_SECRET_KEY", "k")
	t.Setenv("LEDGERBOT_WORKER_CONCURRENCY", "many")
	t.Setenv("LEDGERBOT_RATE_RPS", "fast")
	t.Setenv("LEDGERBOT_TENANT_TTL", "30")
	t.Setenv("LEDGERBOT_SIMULATE_EVERY", "soon")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Dev || c.WorkerConcurrency != 64 || c.RateRPS != 20 || c.TenantTTL != 30*time.Second || c.SimulateEvery != 0 {
		t.Fatalf("invalid values should fall back to defaults: %+v", c)
	}
}

func TestLoadValidation(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"secret required outside dev", map[string]string{}, "LEDGERBOT_SECRET_KEY"},
		{"secret optional in dev", map[string]string{"LEDGERBOT_DEV": "true"}, ""},
		{"http tenant needs host url", map[string]string{"LEDGERBOT_DEV": "true", "LEDGERBOT_TENANT_SOURCE": "http"}, "LEDGERBOT_HOST_API_URL"},
		{"host url of slashes only", map[string]string{"LEDGERBOT_DEV": "true", "LEDGERBOT_TENANT_SOURCE": "http", "LEDGERBOT_HOST_API_URL": "///"}, "LEDGERBOT_HOST_API_URL"},
		{"http tenant with url", map[string]string{"LEDGERBOT_DEV": "true", "LEDGERBOT_TENANT_SOURCE": "http", "LEDGERBOT_HOST_API_URL": "http://h"}, ""},
		{"file tenant ignores host url", map[string]string{"LEDGERBOT_SECRET_KEY": "x"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("err = %v, want mention of %s", err, c.wantErr)
			}
		})
	}
}

func TestEnvTreatsEmptyAsUnset(t *testing.T) {
	t.Setenv("LEDGERBOT_TEST_EMPTY", "")
	if got := env("LEDGERBOT_TEST_EMPTY", "def"); got != "def" {
		t.Fatalf("env = %q", got)
	}
	os.Unsetenv("LEDGERBOT_TEST_EMPTY")
	if got := env("LEDGERBOT_TEST_EMPTY", "def"); got != "def" {
		t.Fatalf("env = %q", got)
	}
	t.Setenv("LEDGERBOT_TEST_EMPTY", " v ")
	if got := env("LEDGERBOT_TEST_EMPTY", "def"); got != " v " {
		t.Fatalf("env should not trim: %q", got)
	}
}
