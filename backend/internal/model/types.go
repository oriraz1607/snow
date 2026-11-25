package model

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AlertState string

const (
	AlertStateFiring       AlertState = "firing"
	AlertStateAcknowledged AlertState = "acknowledged"
	AlertStateDismissed    AlertState = "dismissed"
	AlertStateResolved     AlertState = "resolved"
)

type SnowSyncStatus string

const (
	SnowSyncPending SnowSyncStatus = "pending"
	SnowSyncCreated SnowSyncStatus = "created"
	SnowSyncLinked  SnowSyncStatus = "linked"
	SnowSyncFailed  SnowSyncStatus = "failed"
)

type SnowCallerMode string

const (
	CallerModeSSOUser    SnowCallerMode = "sso_user"
	CallerModeGenericNOC SnowCallerMode = "generic_noc"
)

type SnowCallerResolutionStatus string

const (
	CallerResPending            SnowCallerResolutionStatus = "pending"
	CallerResSSOMatched         SnowCallerResolutionStatus = "sso_matched"
	CallerResGenericNOCSelected SnowCallerResolutionStatus = "generic_noc_selected"
	CallerResFallbackNOC        SnowCallerResolutionStatus = "fallback_noc"
)

type AuditEventType string

const (
	AuditAlertFired           AuditEventType = "alert_fired"
	AuditAlertUpdated         AuditEventType = "alert_updated"
	AuditAlertResolved        AuditEventType = "alert_resolved"
	AuditAlertAcknowledged    AuditEventType = "alert_acknowledged"
	AuditAlertUnacknowledged  AuditEventType = "alert_unacknowledged"
	AuditAlertDismissed       AuditEventType = "alert_dismissed"
	AuditAlertReopened        AuditEventType = "alert_reopened"
	AuditOperatorNoteAdded    AuditEventType = "operator_note_added"
	AuditSnowIncidentReq      AuditEventType = "snow_incident_requested"
	AuditSnowIncidentCreated  AuditEventType = "snow_incident_created"
	AuditSnowIncidentFailed   AuditEventType = "snow_incident_failed"
	AuditSnowIncidentLinked   AuditEventType = "snow_incident_linked"
	AuditSnowCallerFallback   AuditEventType = "snow_caller_fallback"
	AuditSnowStateSynced      AuditEventType = "snow_state_synced"
	AuditSnowWorkNotePosted   AuditEventType = "snow_work_note_posted"
)

type ActorType string

const (
	ActorOperator     ActorType = "operator"
	ActorAlertmanager ActorType = "alertmanager"
	ActorServiceNow   ActorType = "servicenow"
	ActorSystem       ActorType = "system"
)

type IncidentDriftStatus string

const (
	DriftNone               IncidentDriftStatus = "none"
	DriftResolvedOpenTicket IncidentDriftStatus = "resolved_open_ticket"
	DriftActiveClosedTicket IncidentDriftStatus = "active_closed_ticket"
)

const (
	SortStartsAtDesc   = "starts_at_desc"
	SortStartsAtAsc    = "starts_at_asc"
	SortSeverityDesc   = "severity_desc"
	SortResolvedAtDesc = "resolved_at_desc"
)

// Alert represents a single Alertmanager alert episode in PostgreSQL.
type Alert struct {
	ID                  string            `json:"id"`
	Fingerprint         string            `json:"fingerprint"`
	StartsAt            time.Time         `json:"starts_at"`
	EndsAt              *time.Time        `json:"ends_at"`
	State               AlertState        `json:"state"`
	Version             int               `json:"version"`
	AlertName           string            `json:"alert_name"`
	Severity            string            `json:"severity"`
	GroupKey            string            `json:"group_key,omitempty"`
	Receiver            string            `json:"receiver,omitempty"`
	Labels              map[string]string `json:"labels"`
	Annotations         map[string]string `json:"annotations"`
	GeneratorURL        *string           `json:"generator_url"`
	ExternalURL         *string           `json:"external_url,omitempty"`
	AcknowledgedBy      *string           `json:"acknowledged_by"`
	AcknowledgedByEmail *string           `json:"acknowledged_by_email"`
	AcknowledgedAt      *time.Time        `json:"acknowledged_at"`
	DismissedBy         *string           `json:"dismissed_by"`
	DismissedByEmail    *string           `json:"dismissed_by_email,omitempty"`
	DismissedAt         *time.Time        `json:"dismissed_at"`
	DismissReason       *string           `json:"dismiss_reason"`
	ResolvedAt          *time.Time        `json:"resolved_at"`
	FirstReceivedAt     time.Time         `json:"first_received_at,omitempty"`
	LastReceivedAt      time.Time         `json:"last_received_at,omitempty"`
	DeliveryCount       int               `json:"delivery_count,omitempty"`
	PreviousEpisodeCount int              `json:"previous_episode_count"`
	IncidentDriftStatus IncidentDriftStatus `json:"incident_drift_status"`
	ServiceNowIncident  *ServiceNowIncident `json:"servicenow_incident"`
}

// AlertDetail extends Alert with full ingestion metadata and recent audit events.
type AlertDetail struct {
	ID                   string              `json:"id"`
	Fingerprint          string              `json:"fingerprint"`
	StartsAt             time.Time           `json:"starts_at"`
	EndsAt               *time.Time          `json:"ends_at"`
	State                AlertState          `json:"state"`
	Version              int                 `json:"version"`
	AlertName            string              `json:"alert_name"`
	Severity             string              `json:"severity"`
	GroupKey             string              `json:"group_key"`
	Receiver             string              `json:"receiver"`
	Labels               map[string]string   `json:"labels"`
	Annotations          map[string]string   `json:"annotations"`
	GeneratorURL         *string             `json:"generator_url"`
	ExternalURL          *string             `json:"external_url"`
	AcknowledgedBy       *string             `json:"acknowledged_by"`
	AcknowledgedByEmail  *string             `json:"acknowledged_by_email"`
	AcknowledgedAt       *time.Time          `json:"acknowledged_at"`
	DismissedBy          *string             `json:"dismissed_by"`
	DismissedAt          *time.Time          `json:"dismissed_at"`
	DismissReason        *string             `json:"dismiss_reason"`
	ResolvedAt           *time.Time          `json:"resolved_at"`
	DeliveryCount        int                 `json:"delivery_count"`
	FirstReceivedAt      time.Time           `json:"first_received_at"`
	LastReceivedAt       time.Time           `json:"last_received_at"`
	PreviousEpisodeCount int                 `json:"previous_episode_count"`
	IncidentDriftStatus  IncidentDriftStatus `json:"incident_drift_status"`
	ServiceNowIncident   *ServiceNowIncident `json:"servicenow_incident"`
	RecentAuditEvents    []AuditEvent        `json:"recent_audit_events"`
}

// ServiceNowIncident represents the linked ServiceNow record for an alert episode.
type ServiceNowIncident struct {
	ID                     string                     `json:"id"`
	AlertID                string                     `json:"-"`
	CorrelationID          string                     `json:"-"`
	SyncStatus             SnowSyncStatus             `json:"sync_status"`
	SnowSysID              *string                    `json:"snow_sys_id"`
	IncidentNumber         *string                    `json:"incident_number"`
	IncidentURL            *string                    `json:"incident_url"`
	SnowState              *int16                     `json:"snow_state"`
	SnowStateLabel         *string                    `json:"snow_state_label"`
	Impact                 int16                      `json:"-"`
	Urgency                int16                      `json:"-"`
	Priority               *int16                     `json:"priority"`
	ShortDescription       string                     `json:"-"`
	Description            *string                    `json:"-"`
	AssignmentGroup        *string                    `json:"assignment_group"`
	Category               *string                    `json:"-"`
	CallerMode             SnowCallerMode             `json:"caller_mode"`
	CallerResolutionStatus SnowCallerResolutionStatus `json:"caller_resolution_status"`
	CallerSysID            *string                    `json:"caller_sys_id,omitempty"`
	CallerFallbackReason   *string                    `json:"caller_fallback_reason,omitempty"`
	CreatedByLogin         string                     `json:"-"`
	CreatedByEmail         *string                    `json:"-"`
	SnowOpenedAt           *time.Time                 `json:"snow_opened_at,omitempty"`
	SnowResolvedAt         *time.Time                 `json:"snow_resolved_at,omitempty"`
	SnowClosedAt           *time.Time                 `json:"snow_closed_at,omitempty"`
	SnowStateSyncedAt      *time.Time                 `json:"snow_state_synced_at"`
	ResolutionNotePostedAt *time.Time                 `json:"resolution_note_posted_at,omitempty"`
	LastError              *string                    `json:"last_error,omitempty"`
	RetryCount             int                        `json:"-"`
	UpdatedAt              time.Time                  `json:"-"`
}

// AuditEvent represents an entry in alert_audit_log.
type AuditEvent struct {
	ID            int64          `json:"id"`
	AlertID       string         `json:"-"`
	EventType     AuditEventType `json:"event_type"`
	ActorType     ActorType      `json:"actor_type"`
	ActorLogin    string         `json:"actor_login"`
	ActorEmail    *string        `json:"actor_email"`
	ActorRole     *string        `json:"actor_role,omitempty"`
	PreviousState *AlertState    `json:"previous_state"`
	NewState      *AlertState    `json:"new_state"`
	Comment       *string        `json:"comment"`
	Metadata      map[string]any `json:"metadata"`
	CreatedAt     time.Time      `json:"created_at"`
}

// PreviousEpisodeSummary summarizes prior episodes sharing a fingerprint.
type PreviousEpisodeSummary struct {
	ID             string     `json:"id"`
	StartsAt       time.Time  `json:"starts_at"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	State          AlertState `json:"state"`
	IncidentNumber *string    `json:"incident_number"`
	SnowStateLabel *string    `json:"snow_state_label"`
}

// AlertHistoryResponse is returned by GET /api/v1/alerts/{alert_id}/history.
type AlertHistoryResponse struct {
	AlertID          string                   `json:"alert_id"`
	Fingerprint      string                   `json:"fingerprint"`
	AuditEvents      []AuditEvent             `json:"audit_events"`
	PreviousEpisodes []PreviousEpisodeSummary `json:"previous_episodes"`
}

// SummaryCounts holds global alert queue counters.
type SummaryCounts struct {
	Firing                      int64 `json:"firing"`
	Acknowledged                int64 `json:"acknowledged"`
	AcknowledgedWithoutIncident int64 `json:"acknowledged_without_incident"`
	Dismissed                   int64 `json:"dismissed"`
	ResolvedOpenTicket          int64 `json:"resolved_open_ticket"`
}

// ListAlertsResponse is returned by GET /api/v1/alerts.
type ListAlertsResponse struct {
	Summary    SummaryCounts `json:"summary"`
	Items      []Alert       `json:"items"`
	NextCursor *string       `json:"next_cursor,omitempty"`
}

// ListAlertsFilter defines supported filters and pagination for GET /api/v1/alerts.
type ListAlertsFilter struct {
	States        []AlertState
	Severities    []string
	IncidentDrift string
	HasIncident   *bool
	Fingerprint   string
	Labels        map[string]string
	Search        string
	From          *time.Time
	To            *time.Time
	Sort          string
	Limit         int
	Cursor        *PageCursor
}

// PageCursor encodes opaque keyset pagination state.
type PageCursor struct {
	Sort         string     `json:"sort"`
	StartsAt     *time.Time `json:"starts_at,omitempty"`
	ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
	ResolvedNull bool       `json:"resolved_null,omitempty"`
	SeverityRank *int       `json:"severity_rank,omitempty"`
	ID           string     `json:"id"`
}

// EncodeCursor serializes a PageCursor into URL-safe base64.
func EncodeCursor(c PageCursor) (string, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCursor parses an opaque base64 PageCursor.
func DecodeCursor(encoded string) (*PageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		// Fall back to standard base64 with padding if a client sent StdEncoding.
		raw, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			raw, err = base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, fmt.Errorf("invalid cursor encoding")
			}
		}
	}
	var c PageCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("invalid cursor payload")
	}
	if !IsValidUUID(c.ID) {
		return nil, fmt.Errorf("invalid cursor id")
	}
	return &c, nil
}

// AcknowledgeRequest is the payload for POST /api/v1/alerts/{id}/acknowledge, /unacknowledge, and /reopen.
type StateMutationRequest struct {
	Version int    `json:"version"`
	Comment string `json:"comment"`
}

// DismissRequest is the payload for POST /api/v1/alerts/{id}/dismiss.
type DismissRequest struct {
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}

// AddNoteRequest is the payload for POST /api/v1/alerts/{id}/notes.
type AddNoteRequest struct {
	Comment string `json:"comment"`
}

// CreateIncidentRequest is the payload for POST /api/v1/alerts/{id}/servicenow/incident.
type CreateIncidentRequest struct {
	ShortDescription string         `json:"short_description"`
	Description      string         `json:"description"`
	Impact           int16          `json:"impact"`
	Urgency          int16          `json:"urgency"`
	AssignmentGroup  string         `json:"assignment_group"`
	Category         string         `json:"category"`
	CallerMode       SnowCallerMode `json:"caller_mode"`
	WorkNotes        string         `json:"work_notes"`
}

// CreateIncidentResponse is returned on 201 Created for incident creation.
type CreateIncidentResponse struct {
	AlertID            string              `json:"alert_id"`
	ServiceNowIncident *ServiceNowIncident `json:"servicenow_incident"`
	Warnings           []string            `json:"warnings,omitempty"`
}

// LinkIncidentRequest is the payload for POST /api/v1/alerts/{id}/servicenow/link.
type LinkIncidentRequest struct {
	IncidentNumber string `json:"incident_number"`
	PostWorkNote   bool   `json:"post_work_note"`
}

// WorkNoteRequest is the payload for POST /api/v1/alerts/{id}/servicenow/work-notes.
type WorkNoteRequest struct {
	WorkNotes string `json:"work_notes"`
}

// IncidentSyncResponse is returned by /servicenow/link and /servicenow/sync.
type IncidentSyncResponse struct {
	AlertID             string              `json:"alert_id"`
	ServiceNowIncident  *ServiceNowIncident `json:"servicenow_incident"`
	IncidentDriftStatus IncidentDriftStatus `json:"incident_drift_status"`
}

// ServiceNowMetadataResponse is returned by GET /api/v1/servicenow/metadata.
type ServiceNowMetadataResponse struct {
	DefaultAssignmentGroup string                `json:"default_assignment_group"`
	DefaultCategory        string                `json:"default_category"`
	Categories             []string              `json:"categories"`
	DefaultCallerMode      SnowCallerMode        `json:"default_caller_mode"`
	DefaultCallerUsername  string                `json:"default_caller_username"`
	AssignmentGroups       []AssignmentGroupInfo `json:"assignment_groups"`
	SnowReachable          bool                  `json:"snow_reachable"`
}

// AssignmentGroupInfo describes a ServiceNow sys_user_group entry.
type AssignmentGroupInfo struct {
	SysID string `json:"sys_id"`
	Name  string `json:"name"`
}

// SnowUserCacheEntry caches ServiceNow sys_user lookups in PostgreSQL.
type SnowUserCacheEntry struct {
	Email        string
	SnowSysID    *string
	SnowUserName *string
	IsActive     bool
	FetchedAt    time.Time
	ExpiresAt    time.Time
}

// PendingResolutionNote represents a resolved alert whose open ServiceNow ticket still needs a work note.
type PendingResolutionNote struct {
	IncidentID     string
	AlertID        string
	SnowSysID      string
	IncidentNumber string
	AlertName      string
	ResolvedAt     time.Time
}

// GaugeCounts carries current DB counts for Prometheus gauges.
type GaugeCounts struct {
	Firing                  int64
	Acknowledged            int64
	Dismissed               int64
	ResolvedWithOpenTicket  int64
	ActiveWithClosedTicket  int64
}

// WebhookIngestionLogEntry records one webhook delivery outcome.
type WebhookIngestionLogEntry struct {
	Source          string
	GroupKey        *string
	PayloadHash     string
	AlertCount      int
	TruncatedAlerts int
	HTTPStatus      int
	ProcessingMs    int
	ErrorMessage    *string
}

// AlertmanagerWebhookPayload is the Alertmanager v4 webhook body.
type AlertmanagerWebhookPayload struct {
	Version           string              `json:"version"`
	GroupKey          string              `json:"groupKey"`
	TruncatedAlerts   int                 `json:"truncatedAlerts"`
	Status            string              `json:"status"`
	Receiver          string              `json:"receiver"`
	GroupLabels       map[string]string   `json:"groupLabels"`
	CommonLabels      map[string]string   `json:"commonLabels"`
	CommonAnnotations map[string]string   `json:"commonAnnotations"`
	ExternalURL       string              `json:"externalURL"`
	Alerts            []AlertmanagerAlert `json:"alerts"`
}

// AlertmanagerAlert represents one element inside Alertmanager's alerts array.
type AlertmanagerAlert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// NormalizedAlert is a validated and normalized Alertmanager alert ready for reconciliation.
type NormalizedAlert struct {
	Fingerprint  string
	StartsAt     time.Time
	EndsAt       *time.Time
	Status       AlertState
	AlertName    string
	Severity     string
	GroupKey     string
	Receiver     string
	Labels       map[string]string
	Annotations  map[string]string
	GeneratorURL *string
	ExternalURL  *string
}

// AlertmanagerWebhookResponse is returned on 200 OK by POST /api/v1/webhooks/alertmanager.
type AlertmanagerWebhookResponse struct {
	Status                  string `json:"status"`
	Processed               int    `json:"processed"`
	Inserted                int    `json:"inserted"`
	Updated                 int    `json:"updated"`
	Resolved                int    `json:"resolved"`
	Ignored                 int    `json:"ignored"`
	TruncatedAlertsWarning  bool   `json:"truncated_alerts_warning"`
}

// ServiceNowWebhookPayload is sent by ServiceNow Business Rules on incident state changes.
type ServiceNowWebhookPayload struct {
	SysID           string  `json:"sys_id"`
	Number          string  `json:"number"`
	CorrelationID   string  `json:"correlation_id"`
	State           int16   `json:"state"`
	StateLabel      string  `json:"state_label"`
	Priority        *int16  `json:"priority"`
	AssignmentGroup *string `json:"assignment_group"`
	UpdatedBy       string  `json:"updated_by"`
	ResolvedAt      *string `json:"resolved_at"`
	ClosedAt        *string `json:"closed_at"`
	CloseNotes      string  `json:"close_notes"`
}

// NewUUID generates a random RFC 4122 UUIDv4 string using crypto/rand.
func NewUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	var dst [36]byte
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst[:]), nil
}

// IsValidUUID checks canonical lowercase/uppercase 36-character UUID syntax.
func IsValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// NormalizeLabelsOrAnnotations clones a string map cleanly (nil -> empty map).
func NormalizeStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// StringPtrOrNil trims s and returns nil if empty.
func StringPtrOrNil(s string) *string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
