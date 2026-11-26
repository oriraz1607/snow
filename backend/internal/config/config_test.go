package config

import (
	"log/slog"
	"strings"
	"testing"
)

func setMinimalValidEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/noc?sslmode=disable")
	t.Setenv("GRAFANA_PLUGIN_JWT_SECRET", "test-jwt-secret")
	t.Setenv("GRAFANA_PUBLIC_URL", "http://localhost:3000")
	t.Setenv("ALERTMANAGER_WEBHOOK_TOKEN", "am-token")
	t.Setenv("SNOW_INSTANCE_URL", "https://example.service-now.com")
	t.Setenv("SNOW_AUTH_MODE", "basic")
	t.Setenv("SNOW_USERNAME", "svc")
	t.Setenv("SNOW_PASSWORD", "secret")
	t.Setenv("SNOW_DEFAULT_NOC_CALLER_SYS_ID", "noc_001")
}

func TestConfigTLSValidation(t *testing.T) {
	t.Run("rejects TLS_CERT_FILE without TLS_KEY_FILE", func(t *testing.T) {
		setMinimalValidEnv(t)
		t.Setenv("TLS_CERT_FILE", "/etc/certs/tls.crt")
		t.Setenv("TLS_KEY_FILE", "")
		if _, err := Load(); err == nil {
			t.Fatalf("expected error when only TLS_CERT_FILE is set")
		}
	})

	t.Run("rejects TLS_KEY_FILE without TLS_CERT_FILE", func(t *testing.T) {
		setMinimalValidEnv(t)
		t.Setenv("TLS_CERT_FILE", "")
		t.Setenv("TLS_KEY_FILE", "/etc/certs/tls.key")
		if _, err := Load(); err == nil {
			t.Fatalf("expected error when only TLS_KEY_FILE is set")
		}
	})

	t.Run("accepts paired TLS files and reports tls_enabled in LogValue", func(t *testing.T) {
		setMinimalValidEnv(t)
		t.Setenv("TLS_CERT_FILE", "/etc/certs/tls.crt")
		t.Setenv("TLS_KEY_FILE", "/etc/certs/tls.key")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected Load error with paired TLS config: %v", err)
		}
		if cfg.TLSCertFile != "/etc/certs/tls.crt" || cfg.TLSKeyFile != "/etc/certs/tls.key" {
			t.Fatalf("unexpected TLS fields: %+v", cfg)
		}
		var sb strings.Builder
		logger := slog.New(slog.NewJSONHandler(&sb, nil))
		logger.Info("cfg", slog.Any("config", *cfg))
		if !strings.Contains(sb.String(), `"tls_enabled":true`) {
			t.Fatalf("expected tls_enabled:true in redacted log output, got %s", sb.String())
		}
	})
}

func TestConfigHardeningDefaultsAndValidation(t *testing.T) {
	t.Run("defaults AllowLegacyProxyHeaderAuth to false and sets hardening defaults", func(t *testing.T) {
		setMinimalValidEnv(t)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected Load error: %v", err)
		}
		if cfg.AllowLegacyProxyHeaderAuth {
			t.Fatalf("expected AllowLegacyProxyHeaderAuth to default to false")
		}
		if !cfg.RateLimitEnabled {
			t.Fatalf("expected RateLimitEnabled to default to true")
		}
		if cfg.RateLimitAPIRPS != 60 || cfg.RateLimitAPIBurst != 120 {
			t.Fatalf("unexpected API rate limit defaults: rps=%d burst=%d", cfg.RateLimitAPIRPS, cfg.RateLimitAPIBurst)
		}
		if cfg.RateLimitWebhookRPS != 30 || cfg.RateLimitWebhookBurst != 60 {
			t.Fatalf("unexpected webhook rate limit defaults: rps=%d burst=%d", cfg.RateLimitWebhookRPS, cfg.RateLimitWebhookBurst)
		}
		if cfg.TrustProxyHeaders {
			t.Fatalf("expected TrustProxyHeaders to default to false")
		}
		if len(cfg.CORSAllowedOrigins) != 0 || len(cfg.ConfluenceAllowedHosts) != 0 {
			t.Fatalf("expected empty CORS/Confluence allowlists by default")
		}
	})

	t.Run("parses CORS_ALLOWED_ORIGINS and CONFLUENCE_ALLOWED_HOSTS and rejects invalid rate limit", func(t *testing.T) {
		setMinimalValidEnv(t)
		t.Setenv("CORS_ALLOWED_ORIGINS", " https://grafana.corp.example/ , https://noc.corp.example ")
		t.Setenv("CONFLUENCE_ALLOWED_HOSTS", " Confluence.Corp.Example , .atlassian.net ")
		t.Setenv("RATE_LIMIT_API_RPS", "45")
		t.Setenv("RATE_LIMIT_WEBHOOK_BURST", "90")
		t.Setenv("TRUST_PROXY_HEADERS", "true")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected Load error: %v", err)
		}
		if len(cfg.CORSAllowedOrigins) != 2 || cfg.CORSAllowedOrigins[0] != "https://grafana.corp.example" {
			t.Fatalf("unexpected CORSAllowedOrigins: %+v", cfg.CORSAllowedOrigins)
		}
		if len(cfg.ConfluenceAllowedHosts) != 2 || cfg.ConfluenceAllowedHosts[0] != "confluence.corp.example" || cfg.ConfluenceAllowedHosts[1] != ".atlassian.net" {
			t.Fatalf("unexpected ConfluenceAllowedHosts: %+v", cfg.ConfluenceAllowedHosts)
		}
		if cfg.RateLimitAPIRPS != 45 || cfg.RateLimitWebhookBurst != 90 || !cfg.TrustProxyHeaders {
			t.Fatalf("unexpected parsed hardening values: %+v", cfg)
		}

		t.Setenv("RATE_LIMIT_WEBHOOK_RPS", "0")
		if _, err := Load(); err == nil {
			t.Fatalf("expected error for RATE_LIMIT_WEBHOOK_RPS=0")
		}
	})
}
