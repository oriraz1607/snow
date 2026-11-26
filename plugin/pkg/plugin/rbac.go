package plugin

import (
	"net/http"
	"strings"
)

const (
	PermAlertsRead     = "noc-alert-manager-app.alerts:read"
	PermAlertsTriage   = "noc-alert-manager-app.alerts:triage"
	PermIncidentsWrite = "noc-alert-manager-app.incidents:write"
	PermSettingsWrite  = "noc-alert-manager-app.settings:write"
)

// PermissionsForRole maps a Grafana organization role to its default plugin RBAC actions.
func PermissionsForRole(role string) []string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "admin":
		return []string{PermAlertsRead, PermAlertsTriage, PermIncidentsWrite, PermSettingsWrite}
	case "editor":
		return []string{PermAlertsRead, PermAlertsTriage, PermIncidentsWrite}
	case "viewer":
		return []string{PermAlertsRead}
	default:
		return nil
	}
}

// RequiredPermissionForRoute validates the HTTP method and resource path against an explicit
// allowlist of operator API endpoints, returning the required RBAC action when allowed.
func RequiredPermissionForRoute(method, cleanPath string) (string, bool) {
	m := strings.ToUpper(strings.TrimSpace(method))
	p := strings.Trim(strings.TrimSpace(cleanPath), "/")
	if p == "" || strings.Contains(p, "..") || strings.Contains(p, "\\") {
		return "", false
	}

	parts := strings.Split(p, "/")
	if len(parts) < 2 || parts[0] != "v1" {
		return "", false
	}

	switch m {
	case http.MethodGet:
		if len(parts) == 2 && parts[1] == "alerts" {
			return PermAlertsRead, true
		}
		if len(parts) == 3 && parts[1] == "servicenow" && parts[2] == "metadata" {
			return PermAlertsRead, true
		}
		if len(parts) == 3 && parts[1] == "alerts" && isValidResourceSegment(parts[2]) {
			return PermAlertsRead, true
		}
		if len(parts) == 4 && parts[1] == "alerts" && isValidResourceSegment(parts[2]) && parts[3] == "history" {
			return PermAlertsRead, true
		}
	case http.MethodPost:
		if len(parts) == 4 && parts[1] == "alerts" && parts[2] == "bulk" && parts[3] == "dismiss" {
			return PermAlertsTriage, true
		}
		if len(parts) == 4 && parts[1] == "alerts" && isValidResourceSegment(parts[2]) {
			switch parts[3] {
			case "acknowledge", "unacknowledge", "dismiss", "reopen", "notes", "team", "confluence":
				return PermAlertsTriage, true
			}
		}
		if len(parts) == 4 && parts[1] == "servicenow" && parts[2] == "incidents" && parts[3] == "bulk" {
			return PermIncidentsWrite, true
		}
		if len(parts) == 5 && parts[1] == "alerts" && isValidResourceSegment(parts[2]) && parts[3] == "servicenow" {
			switch parts[4] {
			case "incident", "link", "sync", "work-notes":
				return PermIncidentsWrite, true
			}
		}
	}

	return "", false
}

func isValidResourceSegment(seg string) bool {
	if seg == "" || seg == "." || seg == ".." {
		return false
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if c <= ' ' || c == '?' || c == '#' || c == '\\' {
			return false
		}
	}
	return true
}
