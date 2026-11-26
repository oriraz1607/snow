package servicenow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const oauthRefreshEarlyWindow = 60 * time.Second

// AuthProvider supplies HTTP Authorization headers for outbound ServiceNow requests.
type AuthProvider interface {
	AuthorizationHeader(ctx context.Context) (tokenOrHeader string, rawToken string, err error)
	InvalidateToken(ctx context.Context, rawToken string)
	IsOAuth() bool
}

// BasicAuthProvider authenticates requests using HTTP Basic Authentication.
type BasicAuthProvider struct {
	header string
}

// NewBasicAuthProvider constructs a BasicAuthProvider.
func NewBasicAuthProvider(username, password string) *BasicAuthProvider {
	raw := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	return &BasicAuthProvider{header: "Basic " + raw}
}

func (b *BasicAuthProvider) AuthorizationHeader(_ context.Context) (string, string, error) {
	return b.header, "", nil
}

func (b *BasicAuthProvider) InvalidateToken(_ context.Context, _ string) {}

func (b *BasicAuthProvider) IsOAuth() bool { return false }

// OAuthTokenProvider caches an OAuth 2.0 Client Credentials token, refreshes proactively
// 60 seconds prior to expiry under a mutex (singleflight), and supports 401 invalidation.
type OAuthTokenProvider struct {
	tokenURL     string
	clientID     string
	clientSecret string
	scope        string
	httpClient   *http.Client
	now          func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewOAuthTokenProvider constructs an OAuthTokenProvider.
func NewOAuthTokenProvider(tokenURL, clientID, clientSecret, scope string, httpClient *http.Client) *OAuthTokenProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &OAuthTokenProvider{
		tokenURL:     tokenURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		scope:        scope,
		httpClient:   httpClient,
		now:          time.Now,
	}
}

func (o *OAuthTokenProvider) IsOAuth() bool { return true }

// AuthorizationHeader returns a cached or freshly fetched Bearer header.
func (o *OAuthTokenProvider) AuthorizationHeader(ctx context.Context) (string, string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()

	now := o.now()
	if o.token != "" && o.expiresAt.Sub(now) > oauthRefreshEarlyWindow {
		return "Bearer " + o.token, o.token, nil
	}

	if err := o.fetchTokenLocked(ctx); err != nil {
		return "", "", err
	}
	return "Bearer " + o.token, o.token, nil
}

// InvalidateToken clears the cached token if it matches failedToken.
func (o *OAuthTokenProvider) InvalidateToken(_ context.Context, rawToken string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if rawToken == "" || o.token == rawToken {
		o.token = ""
		o.expiresAt = time.Time{}
	}
}

type oauthTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

func (o *OAuthTokenProvider) fetchTokenLocked(ctx context.Context) error {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", o.clientID)
	form.Set("client_secret", o.clientSecret)
	if o.scope != "" {
		form.Set("scope", o.scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build oauth token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := o.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request oauth token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("oauth token endpoint returned HTTP %d", resp.StatusCode)
	}

	var parsed oauthTokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode oauth token response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return fmt.Errorf("oauth token response omitted access_token")
	}

	expiresIn := parsed.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 1800
	}
	o.token = parsed.AccessToken
	o.expiresAt = o.now().Add(time.Duration(expiresIn) * time.Second)
	return nil
}
