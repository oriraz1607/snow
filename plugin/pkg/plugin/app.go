package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
)

const (
	defaultHTTPTimeout    = 30 * time.Second
	healthCheckTimeout    = 5 * time.Second
	maxProxyResponseBytes = 10 << 20 // 10 MiB
)

var (
	_ backend.CallResourceHandler   = (*App)(nil)
	_ backend.CheckHealthHandler    = (*App)(nil)
	_ instancemgmt.InstanceDisposer = (*App)(nil)
)

// App implements Grafana's CallResource and CheckHealth backend contracts as a thin identity proxy.
type App struct {
	settings   Settings
	httpClient *http.Client
	now        func() time.Time
}

// NewApp constructs an App instance from Grafana plugin settings.
func NewApp(_ context.Context, raw backend.AppInstanceSettings) (instancemgmt.Instance, error) {
	settings, err := ParseSettings(raw)
	if err != nil {
		return nil, err
	}
	return &App{
		settings: settings,
		httpClient: &http.Client{
			Timeout: defaultHTTPTimeout,
		},
		now: time.Now,
	}, nil
}

// Dispose cleans up idle HTTP connections when the plugin instance is recycled.
func (a *App) Dispose() {
	if a.httpClient != nil {
		a.httpClient.CloseIdleConnections()
	}
}

// CallResource authenticates the Grafana session user, enforces plugin RBAC, mints a short-lived
// HS256 JWT, and proxies requests under /v1/* to the standalone Alert Manager Go API.
func (a *App) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	if req == nil {
		return sendErrorResponse(sender, http.StatusBadRequest, "INVALID_REQUEST", "Empty resource request.")
	}

	user := req.PluginContext.User
	if user == nil || strings.TrimSpace(user.Login) == "" {
		return sendErrorResponse(sender, http.StatusUnauthorized, "UNAUTHORIZED", "Authenticated Grafana user is required.")
	}

	cleanPath := strings.Trim(strings.TrimSpace(req.Path), "/")
	if strings.Contains(cleanPath, "..") || strings.Contains(cleanPath, "\\") {
		return sendErrorResponse(sender, http.StatusBadRequest, "INVALID_REQUEST", "Invalid resource path.")
	}

	requiredPerm, matched := RequiredPermissionForRoute(req.Method, cleanPath)
	if !matched {
		return sendErrorResponse(sender, http.StatusNotFound, "NOT_FOUND", "Resource endpoint not found.")
	}

	perms := PermissionsForRole(user.Role)
	if !slices.Contains(perms, requiredPerm) {
		return sendErrorResponse(sender, http.StatusForbidden, "FORBIDDEN_RBAC", "Insufficient permissions for requested action.")
	}

	if err := a.settings.ValidateForProxy(); err != nil {
		return sendErrorResponse(sender, http.StatusServiceUnavailable, "PLUGIN_NOT_CONFIGURED", "Alert Manager plugin is not configured: "+err.Error())
	}

	token, err := MintOperatorJWT(a.settings.APIJWTSecret, user, req.PluginContext.OrgID, perms, a.now())
	if err != nil {
		return sendErrorResponse(sender, http.StatusInternalServerError, "INTERNAL_ERROR", "Failed to mint upstream authentication token.")
	}

	targetURL, err := buildTargetURL(a.settings.APIBaseURL, cleanPath, req.URL)
	if err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request URL parameters.")
	}

	upReq, err := http.NewRequestWithContext(ctx, strings.ToUpper(strings.TrimSpace(req.Method)), targetURL, bytes.NewReader(req.Body))
	if err != nil {
		return sendErrorResponse(sender, http.StatusBadRequest, "INVALID_REQUEST", "Failed to construct upstream request.")
	}

	copySafeRequestHeaders(req.Headers, upReq.Header)
	upReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(upReq)
	if err != nil {
		return sendErrorResponse(sender, http.StatusServiceUnavailable, "API_UNAVAILABLE", "Unable to reach Alert Manager API service.")
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyResponseBytes+1))
	if err != nil {
		return sendErrorResponse(sender, http.StatusBadGateway, "UPSTREAM_READ_ERROR", "Failed reading response from Alert Manager API.")
	}
	if len(body) > maxProxyResponseBytes {
		return sendErrorResponse(sender, http.StatusBadGateway, "UPSTREAM_RESPONSE_TOO_LARGE", "Upstream API response exceeded maximum allowed size.")
	}

	outHeaders := make(map[string][]string)
	if ct := strings.TrimSpace(resp.Header.Get("Content-Type")); ct != "" {
		outHeaders["Content-Type"] = []string{ct}
	} else {
		outHeaders["Content-Type"] = []string{"application/json; charset=utf-8"}
	}
	if reqID := strings.TrimSpace(resp.Header.Get("X-Request-Id")); reqID != "" {
		outHeaders["X-Request-Id"] = []string{reqID}
	}

	return sender.Send(&backend.CallResourceResponse{
		Status:  resp.StatusCode,
		Headers: outHeaders,
		Body:    body,
	})
}

// CheckHealth verifies configuration and live authenticated connectivity to the Go API service.
func (a *App) CheckHealth(ctx context.Context, req *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	if err := a.settings.ValidateForProxy(); err != nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "Plugin settings incomplete: " + err.Error(),
		}, nil
	}

	var orgID int64 = 1
	if req != nil && req.PluginContext.OrgID > 0 {
		orgID = req.PluginContext.OrgID
	}

	probeUser := &backend.User{
		Login: "grafana-healthcheck",
		Name:  "Grafana Health Check",
		Role:  "Viewer",
	}
	token, err := MintOperatorJWT(a.settings.APIJWTSecret, probeUser, orgID, []string{PermAlertsRead}, a.now())
	if err != nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "Failed to sign health check token: " + err.Error(),
		}, nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()

	targetURL := a.settings.APIBaseURL + "/api/v1/servicenow/metadata"
	upReq, err := http.NewRequestWithContext(probeCtx, http.MethodGet, targetURL, nil)
	if err != nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "Invalid API URL: " + err.Error(),
		}, nil
	}
	upReq.Header.Set("Accept", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+token)

	resp, err := a.httpClient.Do(upReq)
	if err != nil {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "Unable to reach Alert Manager API: " + err.Error(),
		}, nil
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: "Authentication rejected by Alert Manager API — verify apiJwtSecret matches GRAFANA_PLUGIN_JWT_SECRET.",
		}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusError,
			Message: fmt.Sprintf("Alert Manager API returned HTTP %d.", resp.StatusCode),
		}, nil
	}

	var meta struct {
		SnowReachable bool `json:"snow_reachable"`
	}
	if err := json.Unmarshal(body, &meta); err == nil && !meta.SnowReachable {
		return &backend.CheckHealthResult{
			Status:  backend.HealthStatusOk,
			Message: "Connected to Alert Manager API (ServiceNow currently unreachable).",
		}, nil
	}

	return &backend.CheckHealthResult{
		Status:  backend.HealthStatusOk,
		Message: "Connected to Alert Manager API.",
	}, nil
}

func buildTargetURL(apiBaseURL, cleanPath, reqURL string) (string, error) {
	target := apiBaseURL + "/api/" + cleanPath
	if strings.TrimSpace(reqURL) == "" {
		return target, nil
	}
	parsed, err := url.Parse(reqURL)
	if err != nil {
		return "", err
	}
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	return target, nil
}

func copySafeRequestHeaders(src map[string][]string, dst http.Header) {
	if ct := firstHeaderValue(src, "Content-Type"); ct != "" {
		dst.Set("Content-Type", ct)
	} else {
		dst.Set("Content-Type", "application/json; charset=utf-8")
	}

	if acc := firstHeaderValue(src, "Accept"); acc != "" {
		dst.Set("Accept", acc)
	} else {
		dst.Set("Accept", "application/json")
	}

	if idem := firstHeaderValue(src, "Idempotency-Key"); idem != "" {
		dst.Set("Idempotency-Key", idem)
	}
	if reqID := firstHeaderValue(src, "X-Request-Id"); reqID != "" {
		dst.Set("X-Request-Id", reqID)
	}
}

func firstHeaderValue(headers map[string][]string, target string) string {
	for k, vals := range headers {
		if strings.EqualFold(k, target) && len(vals) > 0 {
			if v := strings.TrimSpace(vals[0]); v != "" {
				return v
			}
		}
	}
	return ""
}

type proxyErrorEnvelope struct {
	Error proxyErrorBody `json:"error"`
}

type proxyErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func sendErrorResponse(sender backend.CallResourceResponseSender, status int, code, message string) error {
	payload, _ := json.Marshal(proxyErrorEnvelope{
		Error: proxyErrorBody{
			Code:    code,
			Message: message,
		},
	})
	return sender.Send(&backend.CallResourceResponse{
		Status: status,
		Headers: map[string][]string{
			"Content-Type": {"application/json; charset=utf-8"},
		},
		Body: payload,
	})
}
