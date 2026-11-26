package model

import (
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const MaxConfluenceURLLength = 2048

const (
	ConfluenceSourceOverride   = "noc_override"
	ConfluenceSourceAnnotation = "annotation"
	ConfluenceSourceRunbook    = "runbook_url"
	ConfluenceSourceNone       = "none"
)

// ValidateConfluenceURL strictly validates and normalizes an HTTPS Confluence documentation URL.
func ValidateConfluenceURL(raw string, allowedHosts []string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("confluence_url is required")
	}
	if len(trimmed) > MaxConfluenceURLLength {
		return "", fmt.Errorf("confluence_url must be at most %d characters", MaxConfluenceURLLength)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("confluence_url must not contain whitespace or control characters")
		}
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("confluence_url is not a valid URL")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", fmt.Errorf("confluence_url must use the https:// scheme")
	}
	if u.User != nil {
		return "", fmt.Errorf("confluence_url must not include embedded credentials")
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return "", fmt.Errorf("confluence_url must include a valid hostname")
	}
	if !hostAllowed(host, allowedHosts) {
		return "", fmt.Errorf("confluence_url host %q is not in CONFLUENCE_ALLOWED_HOSTS", host)
	}

	u.Scheme = "https"
	return u.String(), nil
}

// ResolveConfluenceURL determines the effective validated HTTPS documentation URL and its source
// using the priority chain: noc_override -> annotations.confluence_url -> annotations.runbook_url.
func ResolveConfluenceURL(override *string, annotations map[string]string, allowedHosts []string) (*string, string) {
	if override != nil && strings.TrimSpace(*override) != "" {
		if norm, err := ValidateConfluenceURL(*override, allowedHosts); err == nil {
			return &norm, ConfluenceSourceOverride
		}
	}
	if annotations != nil {
		if annURL := strings.TrimSpace(annotations["confluence_url"]); annURL != "" {
			if norm, err := ValidateConfluenceURL(annURL, allowedHosts); err == nil {
				return &norm, ConfluenceSourceAnnotation
			}
		}
		if runbook := strings.TrimSpace(annotations["runbook_url"]); runbook != "" {
			if norm, err := ValidateConfluenceURL(runbook, allowedHosts); err == nil {
				return &norm, ConfluenceSourceRunbook
			}
		}
	}
	return nil, ConfluenceSourceNone
}

func hostAllowed(host string, allowedHosts []string) bool {
	if len(allowedHosts) == 0 {
		return true
	}
	for _, pattern := range allowedHosts {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) && len(host) > len(p) {
				return true
			}
			continue
		}
		if host == p {
			return true
		}
	}
	return false
}
