package auth

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
)

const (
	ExpectedAudience = "noc-alert-api"
	MaxJWTClockSkew  = 5 * time.Second
	MaxJWTLifetime   = 120 * time.Second
)

// OperatorClaims defines the internal JWT payload minted by the Grafana App Plugin backend.
type OperatorClaims struct {
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	OrgID       int64    `json:"org_id"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	jwt.RegisteredClaims
}

// OperatorAuthConfig configures operator JWT and optional legacy proxy header authentication.
type OperatorAuthConfig struct {
	JWTSecret                string
	JWTSecretPrevious        string
	JWTIssuer                string
	AllowLegacyProxyFallback bool
	ProxySharedToken         string
}

// OperatorMiddleware returns HTTP middleware authenticating operator API requests.
func OperatorMiddleware(cfg OperatorAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
			if authHeader != "" {
				tokenStr, ok := parseBearerToken(authHeader)
				if !ok {
					apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Invalid Authorization header format."))
					return
				}
				op, err := verifyOperatorJWT(tokenStr, cfg)
				if err != nil {
					apierror.Write(w, r, err)
					return
				}
				next.ServeHTTP(w, r.WithContext(WithOperator(r.Context(), op)))
				return
			}

			if cfg.AllowLegacyProxyFallback {
				proxyToken := r.Header.Get("X-Proxy-Token")
				grafanaUser := strings.TrimSpace(r.Header.Get("X-Grafana-User"))
				if proxyToken != "" && grafanaUser != "" && cfg.ProxySharedToken != "" {
					if subtle.ConstantTimeCompare([]byte(proxyToken), []byte(cfg.ProxySharedToken)) == 1 {
						// X-Grafana-Role is strictly trimmed and never defaulted to an elevated role;
						// RequirePermission / HasPermission fails closed when X-Grafana-Role is empty.
						op := OperatorContext{
							Login:            grafanaUser,
							Name:             grafanaUser,
							Role:             strings.TrimSpace(r.Header.Get("X-Grafana-Role")),
							LookupByUsername: true,
						}
						next.ServeHTTP(w, r.WithContext(WithOperator(r.Context(), op)))
						return
					}
					apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Invalid proxy authentication token."))
					return
				}
			}

			apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Missing operator authentication credentials."))
		})
	}
}

func parseBearerToken(header string) (string, bool) {
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	tok := strings.TrimSpace(parts[1])
	return tok, tok != ""
}

func verifyOperatorJWT(tokenStr string, cfg OperatorAuthConfig) (OperatorContext, error) {
	secrets := make([]string, 0, 2)
	if cfg.JWTSecret != "" {
		secrets = append(secrets, cfg.JWTSecret)
	}
	if cfg.JWTSecretPrevious != "" {
		secrets = append(secrets, cfg.JWTSecretPrevious)
	}
	if len(secrets) == 0 {
		return OperatorContext{}, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "JWT authentication is not configured.")
	}

	var lastErr error
	for _, secret := range secrets {
		op, err := parseAndValidateJWT(tokenStr, secret, cfg.JWTIssuer)
		if err == nil {
			return op, nil
		}
		lastErr = err
		// If signature was invalid and a previous rotation key exists, try the next key;
		// for claim errors like expiration, fail immediately.
		if !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			break
		}
	}

	if errors.Is(lastErr, jwt.ErrTokenExpired) {
		return OperatorContext{}, apierror.New(http.StatusUnauthorized, apierror.CodeTokenExpired, "Operator token has expired.").WithCause(lastErr)
	}
	return OperatorContext{}, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Invalid operator authentication token.").WithCause(lastErr)
}

func parseAndValidateJWT(tokenStr, secret, issuer string) (OperatorContext, error) {
	claims := &OperatorClaims{}
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithAudience(ExpectedAudience),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(MaxJWTClockSkew),
	}

	token, err := jwt.ParseWithClaims(tokenStr, claims, func(_ *jwt.Token) (any, error) {
		return []byte(secret), nil
	}, parserOpts...)
	if err != nil {
		return OperatorContext{}, err
	}
	if !token.Valid {
		return OperatorContext{}, fmt.Errorf("token invalid")
	}

	if claims.IssuedAt == nil || claims.ExpiresAt == nil {
		return OperatorContext{}, fmt.Errorf("missing iat or exp claim")
	}
	ttl := claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time)
	if ttl < 0 || ttl > MaxJWTLifetime {
		return OperatorContext{}, fmt.Errorf("token lifetime %s exceeds maximum %s", ttl, MaxJWTLifetime)
	}

	login := strings.TrimSpace(claims.Subject)
	if login == "" {
		return OperatorContext{}, fmt.Errorf("missing sub claim")
	}

	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = login
	}

	return OperatorContext{
		Login:       login,
		Email:       strings.TrimSpace(claims.Email),
		Name:        name,
		Role:        strings.TrimSpace(claims.Role),
		Permissions: claims.Permissions,
		OrgID:       claims.OrgID,
	}, nil
}
