// Package config reads ledgerbot's environment configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the process configuration (see .env.example).
type Config struct {
	DatabaseURL string
	DBMaxConns  int32
	HTTPAddr    string
	PublicURL   string

	TenantSource string // "file" or "http"
	SeedDir      string
	HostAPIURL   string
	HostAPIToken string
	TenantTTL    time.Duration

	DataDriver    string // "fake" or "api"
	ProvidersFile string
	FXSource      string // "tcmb" or "static"

	SecretKey     string
	AdminToken    string
	MetaAppSecret string
	Dev           bool

	WorkerConcurrency  int
	PerCompanyInflight int
	SimulateEvery      time.Duration
	MaxListeners       int

	RateRPS   float64
	RateBurst int
	LogLevel  string
}

// Load reads the environment.
func Load() (Config, error) {
	c := Config{
		DatabaseURL:        env("LEDGERBOT_DATABASE_URL", "postgres://ledgerbot:ledgerbot@localhost:5432/ledgerbot?sslmode=disable"),
		DBMaxConns:         int32(min(max(envInt("LEDGERBOT_DB_MAX_CONNS", 40), 1), 1000)), //nolint:gosec // clamped
		HTTPAddr:           env("LEDGERBOT_HTTP_ADDR", ":8080"),
		PublicURL:          env("LEDGERBOT_PUBLIC_URL", "http://localhost:8080"),
		TenantSource:       env("LEDGERBOT_TENANT_SOURCE", "file"),
		SeedDir:            env("LEDGERBOT_SEED_DIR", "seed/companies"),
		HostAPIURL:         strings.TrimRight(env("LEDGERBOT_HOST_API_URL", ""), "/"),
		HostAPIToken:       env("LEDGERBOT_HOST_API_TOKEN", ""),
		TenantTTL:          envDur("LEDGERBOT_TENANT_TTL", 30*time.Second),
		DataDriver:         env("LEDGERBOT_DATA_DRIVER", "fake"),
		ProvidersFile:      env("LEDGERBOT_PROVIDERS_FILE", ""),
		FXSource:           env("LEDGERBOT_FX_SOURCE", "tcmb"),
		SecretKey:          env("LEDGERBOT_SECRET_KEY", ""),
		AdminToken:         env("LEDGERBOT_ADMIN_TOKEN", ""),
		MetaAppSecret:      env("LEDGERBOT_META_APP_SECRET", ""),
		Dev:                envBool("LEDGERBOT_DEV", false),
		WorkerConcurrency:  envInt("LEDGERBOT_WORKER_CONCURRENCY", 64),
		PerCompanyInflight: envInt("LEDGERBOT_PER_COMPANY_INFLIGHT", 4),
		SimulateEvery:      envDur("LEDGERBOT_SIMULATE_EVERY", 0),
		MaxListeners:       envInt("LEDGERBOT_MAX_LISTENERS", 1000),
		RateRPS:            envFloat("LEDGERBOT_RATE_RPS", 20),
		RateBurst:          envInt("LEDGERBOT_RATE_BURST", 40),
		LogLevel:           env("LEDGERBOT_LOG_LEVEL", "info"),
	}
	if c.TenantSource == "http" && c.HostAPIURL == "" {
		return c, fmt.Errorf("config: LEDGERBOT_HOST_API_URL is required when LEDGERBOT_TENANT_SOURCE=http")
	}
	if c.SecretKey == "" && !c.Dev {
		return c, fmt.Errorf("config: LEDGERBOT_SECRET_KEY (64 hex chars) is required outside dev mode")
	}
	return c, nil
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(env(k, "")); err == nil {
		return v
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(env(k, ""), 64); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(env(k, "")); err == nil {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(env(k, "")); err == nil {
		return v
	}
	return def
}
