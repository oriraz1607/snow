package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	SnowAuthOAuth = "oauth_client_credentials"
	SnowAuthBasic = "basic"
)

// Config holds validated runtime configuration loaded strictly from environment variables.
type Config struct {
	Port                         string
	AdminPort                    string
	LogLevel                     string
	DatabaseURL                  string
	PGMaxConns                   int32
	PGMinConns                   int32
	JWTSecret                    string
	JWTSecretPrevious            string
	JWTIssuer                    string
	GrafanaPublicURL             string
	AllowLegacyProxyHeaderAuth   bool
	ProxySharedToken             string
	AlertmanagerWebhookToken     string
	AlertmanagerMaxPayloadBytes  int64
	SnowInstanceURL              string
	SnowAuthMode                 string
	SnowTokenURL                 string
	SnowClientID                 string
	SnowClientSecret             string
	SnowOAuthScope               string
	SnowUsername                 string
	SnowPassword                 string
	DefaultCallerSysID           string
	DefaultCallerUsername        string
	DefaultAssignmentGroup       string
	DefaultCategory              string
	SnowInboundWebhookSecret     string
	SnowHTTPTimeout              time.Duration
	SnowPollInterval             time.Duration
	SnowAutoPostResolvedNote     bool
	SnowNoteWorkerInterval       time.Duration
	SnowAutoReopenCooldown       time.Duration
	SnowAutoReopenMaxAttempts    int
	AllowRetrospectiveIncident  bool
	TLSCertFile                 string
	TLSKeyFile                  string
	RateLimitEnabled            bool
	RateLimitAPIRPS             int
	RateLimitAPIBurst           int
	RateLimitWebhookRPS         int
	RateLimitWebhookBurst       int
	TrustProxyHeaders           bool
	CORSAllowedOrigins          []string
	ConfluenceAllowedHosts      []string
}

// Load parses and validates environment variables into Config.
func Load() (*Config, error) {
	cfg := &Config{
		Port:                     envOrDefault("PORT", "8080"),
		AdminPort:                envOrDefault("ADMIN_PORT", "9090"),
		LogLevel:                 envOrDefault("LOG_LEVEL", "info"),
		DatabaseURL:              strings.TrimSpace(os.Getenv("DATABASE_URL")),
		JWTSecret:                os.Getenv("GRAFANA_PLUGIN_JWT_SECRET"),
		JWTSecretPrevious:        os.Getenv("GRAFANA_PLUGIN_JWT_SECRET_PREVIOUS"),
		JWTIssuer:                envOrDefault("GRAFANA_PLUGIN_JWT_ISSUER", "noc-alert-manager-app"),
		GrafanaPublicURL:         strings.TrimRight(strings.TrimSpace(os.Getenv("GRAFANA_PUBLIC_URL")), "/"),
		ProxySharedToken:         os.Getenv("GRAFANA_PROXY_SHARED_TOKEN"),
		AlertmanagerWebhookToken: os.Getenv("ALERTMANAGER_WEBHOOK_TOKEN"),
		SnowInstanceURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("SNOW_INSTANCE_URL")), "/"),
		SnowAuthMode:             strings.ToLower(envOrDefault("SNOW_AUTH_MODE", SnowAuthOAuth)),
		SnowTokenURL:             strings.TrimSpace(os.Getenv("SNOW_TOKEN_URL")),
		SnowClientID:             strings.TrimSpace(os.Getenv("SNOW_CLIENT_ID")),
		SnowClientSecret:         os.Getenv("SNOW_CLIENT_SECRET"),
		SnowOAuthScope:           strings.TrimSpace(os.Getenv("SNOW_OAUTH_SCOPE")),
		SnowUsername:             strings.TrimSpace(os.Getenv("SNOW_USERNAME")),
		SnowPassword:             os.Getenv("SNOW_PASSWORD"),
		DefaultCallerSysID:       strings.TrimSpace(os.Getenv("SNOW_DEFAULT_NOC_CALLER_SYS_ID")),
		DefaultCallerUsername:    envOrDefault("SNOW_DEFAULT_NOC_CALLER_USERNAME", "noc"),
		DefaultAssignmentGroup:   strings.TrimSpace(os.Getenv("SNOW_DEFAULT_ASSIGNMENT_GROUP")),
		DefaultCategory:          envOrDefault("SNOW_DEFAULT_CATEGORY", "Infrastructure"),
		SnowInboundWebhookSecret: os.Getenv("SNOW_INBOUND_WEBHOOK_SECRET"),
		TLSCertFile:              strings.TrimSpace(os.Getenv("TLS_CERT_FILE")),
		TLSKeyFile:               strings.TrimSpace(os.Getenv("TLS_KEY_FILE")),
		CORSAllowedOrigins:       parseCSVEnv("CORS_ALLOWED_ORIGINS", false),
		ConfluenceAllowedHosts:   parseCSVEnv("CONFLUENCE_ALLOWED_HOSTS", true),
	}

	var err error
	if cfg.PGMaxConns, err = envInt32("PG_MAX_CONNS", 25, 1); err != nil {
		return nil, err
	}
	if cfg.PGMinConns, err = envInt32("PG_MIN_CONNS", 5, 0); err != nil {
		return nil, err
	}
	if cfg.PGMinConns > cfg.PGMaxConns {
		return nil, fmt.Errorf("PG_MIN_CONNS (%d) cannot exceed PG_MAX_CONNS (%d)", cfg.PGMinConns, cfg.PGMaxConns)
	}
	if cfg.AllowLegacyProxyHeaderAuth, err = envBool("ALLOW_LEGACY_PROXY_HEADER_AUTH", false); err != nil {
		return nil, err
	}
	if cfg.AlertmanagerMaxPayloadBytes, err = envInt64("ALERTMANAGER_MAX_PAYLOAD_BYTES", 5*1024*1024, 1024); err != nil {
		return nil, err
	}
	httpTimeoutSec, err := envInt64("SNOW_HTTP_TIMEOUT_SECONDS", 15, 1)
	if err != nil {
		return nil, err
	}
	cfg.SnowHTTPTimeout = time.Duration(httpTimeoutSec) * time.Second

	pollSec, err := envInt64("SNOW_POLL_INTERVAL_SECONDS", 120, 1)
	if err != nil {
		return nil, err
	}
	cfg.SnowPollInterval = time.Duration(pollSec) * time.Second

	noteWorkerSec, err := envInt64("SNOW_NOTE_WORKER_INTERVAL_SECONDS", 15, 1)
	if err != nil {
		return nil, err
	}
	cfg.SnowNoteWorkerInterval = time.Duration(noteWorkerSec) * time.Second

	reopenCooldownSec, err := envInt64("SNOW_AUTO_REOPEN_COOLDOWN_SECONDS", 300, 1)
	if err != nil {
		return nil, err
	}
	cfg.SnowAutoReopenCooldown = time.Duration(reopenCooldownSec) * time.Second

	reopenMaxAttempts, err := envInt32("SNOW_AUTO_REOPEN_MAX_ATTEMPTS", 3, 0)
	if err != nil {
		return nil, err
	}
	cfg.SnowAutoReopenMaxAttempts = int(reopenMaxAttempts)

	if cfg.SnowAutoPostResolvedNote, err = envBool("SNOW_AUTO_POST_RESOLVED_NOTE", true); err != nil {
		return nil, err
	}
	if cfg.AllowRetrospectiveIncident, err = envBool("ALLOW_RETROSPECTIVE_INCIDENT_CREATE", false); err != nil {
		return nil, err
	}
	if cfg.RateLimitEnabled, err = envBool("RATE_LIMIT_ENABLED", true); err != nil {
		return nil, err
	}
	apiRPS, err := envInt32("RATE_LIMIT_API_RPS", 60, 1)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitAPIRPS = int(apiRPS)
	apiBurst, err := envInt32("RATE_LIMIT_API_BURST", 120, 1)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitAPIBurst = int(apiBurst)
	whRPS, err := envInt32("RATE_LIMIT_WEBHOOK_RPS", 30, 1)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitWebhookRPS = int(whRPS)
	whBurst, err := envInt32("RATE_LIMIT_WEBHOOK_BURST", 60, 1)
	if err != nil {
		return nil, err
	}
	cfg.RateLimitWebhookBurst = int(whBurst)
	if cfg.TrustProxyHeaders, err = envBool("TRUST_PROXY_HEADERS", false); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	if c.JWTSecret == "" && !c.AllowLegacyProxyHeaderAuth {
		return fmt.Errorf("GRAFANA_PLUGIN_JWT_SECRET is required when ALLOW_LEGACY_PROXY_HEADER_AUTH is false")
	}
	if c.AllowLegacyProxyHeaderAuth && c.ProxySharedToken == "" {
		return fmt.Errorf("GRAFANA_PROXY_SHARED_TOKEN is required when ALLOW_LEGACY_PROXY_HEADER_AUTH is true")
	}
	if c.GrafanaPublicURL == "" {
		return fmt.Errorf("GRAFANA_PUBLIC_URL is required")
	}
	if c.AlertmanagerWebhookToken == "" {
		return fmt.Errorf("ALERTMANAGER_WEBHOOK_TOKEN is required")
	}
	if c.SnowInstanceURL == "" {
		return fmt.Errorf("SNOW_INSTANCE_URL is required")
	}
	if c.DefaultCallerSysID == "" {
		return fmt.Errorf("SNOW_DEFAULT_NOC_CALLER_SYS_ID is required")
	}

	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("TLS_CERT_FILE and TLS_KEY_FILE must both be set when configuring in-process TLS")
	}

	switch c.SnowAuthMode {
	case SnowAuthOAuth:
		if c.SnowTokenURL == "" {
			c.SnowTokenURL = c.SnowInstanceURL + "/oauth_token.do"
		}
		if c.SnowClientID == "" || c.SnowClientSecret == "" {
			return fmt.Errorf("SNOW_CLIENT_ID and SNOW_CLIENT_SECRET are required for oauth_client_credentials")
		}
	case SnowAuthBasic:
		if c.SnowUsername == "" || c.SnowPassword == "" {
			return fmt.Errorf("SNOW_USERNAME and SNOW_PASSWORD are required for basic auth mode")
		}
	default:
		return fmt.Errorf("unsupported SNOW_AUTH_MODE %q (expected %q or %q)", c.SnowAuthMode, SnowAuthOAuth, SnowAuthBasic)
	}
	return nil
}

// LogValue redacts all secrets if Config is ever passed to slog.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("port", c.Port),
		slog.String("admin_port", c.AdminPort),
		slog.String("log_level", c.LogLevel),
		slog.String("database_url", "[REDACTED]"),
		slog.Int("pg_max_conns", int(c.PGMaxConns)),
		slog.Int("pg_min_conns", int(c.PGMinConns)),
		slog.String("jwt_issuer", c.JWTIssuer),
		slog.Bool("jwt_secret_set", c.JWTSecret != ""),
		slog.Bool("jwt_previous_secret_set", c.JWTSecretPrevious != ""),
		slog.String("grafana_public_url", c.GrafanaPublicURL),
		slog.Bool("allow_legacy_proxy_header_auth", c.AllowLegacyProxyHeaderAuth),
		slog.String("snow_instance_url", c.SnowInstanceURL),
		slog.String("snow_auth_mode", c.SnowAuthMode),
		slog.String("default_caller_username", c.DefaultCallerUsername),
		slog.Duration("snow_http_timeout", c.SnowHTTPTimeout),
		slog.Duration("snow_poll_interval", c.SnowPollInterval),
		slog.Duration("snow_auto_reopen_cooldown", c.SnowAutoReopenCooldown),
		slog.Int("snow_auto_reopen_max_attempts", c.SnowAutoReopenMaxAttempts),
		slog.Bool("snow_auto_post_resolved_note", c.SnowAutoPostResolvedNote),
		slog.Bool("tls_enabled", c.TLSCertFile != "" && c.TLSKeyFile != ""),
		slog.Bool("rate_limit_enabled", c.RateLimitEnabled),
		slog.Int("rate_limit_api_rps", c.RateLimitAPIRPS),
		slog.Int("rate_limit_webhook_rps", c.RateLimitWebhookRPS),
		slog.Bool("trust_proxy_headers", c.TrustProxyHeaders),
		slog.Int("cors_allowed_origins_count", len(c.CORSAllowedOrigins)),
		slog.Int("confluence_allowed_hosts_count", len(c.ConfluenceAllowedHosts)),
	)
}

func parseCSVEnv(key string, lowercase bool) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		v := strings.TrimSpace(part)
		if v == "" {
			continue
		}
		if lowercase {
			v = strings.ToLower(v)
		} else {
			v = strings.TrimRight(v, "/")
		}
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid boolean for %s: %w", key, err)
	}
	return b, nil
}

func envInt32(key string, def, minVal int32) (int32, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || int32(n) < minVal {
		return 0, fmt.Errorf("invalid integer for %s (min %d)", key, minVal)
	}
	return int32(n), nil
}

func envInt64(key string, def, minVal int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < minVal {
		return 0, fmt.Errorf("invalid integer for %s (min %d)", key, minVal)
	}
	return n, nil
}
