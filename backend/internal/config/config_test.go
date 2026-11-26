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
