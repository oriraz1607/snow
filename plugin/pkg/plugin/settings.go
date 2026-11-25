package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

const (
	DefaultPollIntervalSeconds    = 15
	MinPollIntervalSeconds        = 5
	MaxPollIntervalSeconds        = 120
	DefaultCallerMode             = "sso_user"
	DefaultGenericNocDisplayLabel = "Fallback Service Account (noc)"
)

// Settings holds plugin configuration persisted in Grafana jsonData and secureJsonData.
type Settings struct {
	APIBaseURL             string `json:"apiBaseUrl"`
	PollIntervalSeconds    int    `json:"pollIntervalSeconds"`
	DefaultCallerMode      string `json:"defaultCallerMode"`
	GenericNocDisplayLabel string `json:"genericNocDisplayLabel"`
	APIJWTSecret           string `json:"-"`
}

// ParseSettings extracts plugin configuration from Grafana's AppInstanceSettings.
func ParseSettings(raw backend.AppInstanceSettings) (Settings, error) {
	s := Settings{
		PollIntervalSeconds:    DefaultPollIntervalSeconds,
		DefaultCallerMode:      DefaultCallerMode,
		GenericNocDisplayLabel: DefaultGenericNocDisplayLabel,
	}

	if len(raw.JSONData) > 0 && string(raw.JSONData) != "null" {
		if err := json.Unmarshal(raw.JSONData, &s); err != nil {
			return Settings{}, fmt.Errorf("invalid plugin jsonData: %w", err)
		}
	}

	s.APIBaseURL = strings.TrimRight(strings.TrimSpace(s.APIBaseURL), "/")

	if s.PollIntervalSeconds == 0 {
		s.PollIntervalSeconds = DefaultPollIntervalSeconds
	} else if s.PollIntervalSeconds < MinPollIntervalSeconds {
		s.PollIntervalSeconds = MinPollIntervalSeconds
	} else if s.PollIntervalSeconds > MaxPollIntervalSeconds {
		s.PollIntervalSeconds = MaxPollIntervalSeconds
	}

	switch strings.TrimSpace(s.DefaultCallerMode) {
	case "generic_noc":
		s.DefaultCallerMode = "generic_noc"
	default:
		s.DefaultCallerMode = DefaultCallerMode
	}

	if label := strings.TrimSpace(s.GenericNocDisplayLabel); label != "" {
		s.GenericNocDisplayLabel = label
	} else {
		s.GenericNocDisplayLabel = DefaultGenericNocDisplayLabel
	}

	if raw.DecryptedSecureJSONData != nil {
		s.APIJWTSecret = strings.TrimSpace(raw.DecryptedSecureJSONData["apiJwtSecret"])
	}

	return s, nil
}

// ValidateForProxy checks that apiBaseUrl and apiJwtSecret are configured for upstream requests.
func (s Settings) ValidateForProxy() error {
	if s.APIBaseURL == "" {
		return errors.New("apiBaseUrl is not configured")
	}
	parsed, err := url.Parse(s.APIBaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("apiBaseUrl must be a valid http or https URL")
	}
	if s.APIJWTSecret == "" {
		return errors.New("apiJwtSecret is not configured")
	}
	return nil
}
