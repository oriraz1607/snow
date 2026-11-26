package auth

import (
	"net/http"
	"slices"
	"strings"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
)

const (
	PermAlertsRead     = "noc-alert-manager-app.alerts:read"
	PermAlertsTriage   = "noc-alert-manager-app.alerts:triage"
	PermIncidentsWrite = "noc-alert-manager-app.incidents:write"
)

// RequirePermission enforces action-level RBAC on authenticated operator requests.
// When explicit JWT permissions are present, they are checked strictly; when omitted,
// authorization falls back to the Grafana role.
func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := OperatorFromContext(r.Context())
			if !ok {
				apierror.Write(w, r, apierror.New(http.StatusUnauthorized, apierror.CodeUnauthorized, "Missing operator context."))
				return
			}

			if !HasPermission(op, permission) {
				apierror.Write(w, r, apierror.New(http.StatusForbidden, apierror.CodeForbiddenRBAC, "Insufficient permissions for requested action."))
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// HasPermission evaluates RBAC access for the given OperatorContext.
// When explicit JWT permissions are omitted, authorization falls back to the Grafana role;
// empty or unknown roles always fail closed (including in legacy proxy fallback mode).
func HasPermission(op OperatorContext, required string) bool {
	if len(op.Permissions) > 0 {
		return slices.Contains(op.Permissions, required)
	}

	switch strings.ToLower(strings.TrimSpace(op.Role)) {
	case "admin", "editor":
		return required == PermAlertsRead || required == PermAlertsTriage || required == PermIncidentsWrite
	case "viewer":
		return required == PermAlertsRead
	default:
		return false
	}
}
