package plugin

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

const (
	JWTIssuer   = "noc-alert-manager-app"
	JWTAudience = "noc-alert-api"
	JWTTTL      = 60 * time.Second
)

// OperatorClaims defines the short-lived internal JWT claims expected by the Go API service.
type OperatorClaims struct {
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	OrgID       int64    `json:"org_id"`
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
	jwt.RegisteredClaims
}

// MintOperatorJWT signs a 60-second HS256 JWT derived solely from Grafana's verified PluginContext.User.
func MintOperatorJWT(secret string, user *backend.User, orgID int64, perms []string, now time.Time) (string, error) {
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("jwt signing secret is empty")
	}
	if user == nil {
		return "", errors.New("authenticated user is required")
	}
	login := strings.TrimSpace(user.Login)
	if login == "" {
		return "", errors.New("authenticated user login is empty")
	}

	name := strings.TrimSpace(user.Name)
	if name == "" {
		name = login
	}

	issuedAt := now.UTC()
	expiresAt := issuedAt.Add(JWTTTL)

	claims := OperatorClaims{
		Email:       strings.TrimSpace(user.Email),
		Name:        name,
		OrgID:       orgID,
		Role:        strings.TrimSpace(user.Role),
		Permissions: slices.Clone(perms),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    JWTIssuer,
			Subject:   login,
			Audience:  jwt.ClaimStrings{JWTAudience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ID:        uuid.NewString(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
