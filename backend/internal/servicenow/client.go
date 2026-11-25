package servicenow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

const userLookupTimeout = 3 * time.Second

// SnowError wraps an APIError with raw ServiceNow HTTP status/body for targeted retry inspection.
type SnowError struct {
	API            *apierror.APIError
	UpstreamStatus int
	UpstreamBody   string
}

func (e *SnowError) Error() string {
	if e == nil || e.API == nil {
		return ""
	}
	return e.API.Error()
}

func (e *SnowError) Unwrap() error {
	if e == nil || e.API == nil {
		return nil
	}
	return e.API
}

// IsCallerIDRejection reports whether ServiceNow rejected the request because of caller_id.
func (e *SnowError) IsCallerIDRejection() bool {
	if e == nil {
		return false
	}
	if e.UpstreamStatus != http.StatusBadRequest && e.UpstreamStatus != http.StatusForbidden && e.UpstreamStatus != http.StatusUnprocessableEntity {
		return false
	}
	return strings.Contains(strings.ToLower(e.UpstreamBody), "caller_id")
}

// SysUser represents a ServiceNow sys_user record.
type SysUser struct {
	SysID    string
	UserName string
	Email    string
}

// CreateIncidentPayload is the JSON payload sent to POST /api/now/table/incident.
type CreateIncidentPayload struct {
	ShortDescription   string `json:"short_description"`
	Description        string `json:"description"`
	Impact             string `json:"impact"`
	Urgency            string `json:"urgency"`
	AssignmentGroup    string `json:"assignment_group,omitempty"`
	Category           string `json:"category,omitempty"`
	CallerID           string `json:"caller_id"`
	CorrelationID      string `json:"correlation_id"`
	CorrelationDisplay string `json:"correlation_display"`
	ContactType        string `json:"contact_type"`
	WorkNotes          string `json:"work_notes,omitempty"`
}

// IncidentRecord is a parsed ServiceNow incident record.
type IncidentRecord struct {
	SysID           string
	Number          string
	IncidentURL     string
	State           int16
	StateLabel      string
	Priority        *int16
	AssignmentGroup *string
	OpenedAt        *time.Time
	ResolvedAt      *time.Time
	ClosedAt        *time.Time
}

func (r IncidentRecord) ToCreateResult() store.SnowCreateResult {
	return store.SnowCreateResult{
		SysID:           r.SysID,
		Number:          r.Number,
		IncidentURL:     r.IncidentURL,
		State:           r.State,
		StateLabel:      r.StateLabel,
		Priority:        r.Priority,
		AssignmentGroup: r.AssignmentGroup,
		OpenedAt:        r.OpenedAt,
		ResolvedAt:      r.ResolvedAt,
		ClosedAt:        r.ClosedAt,
	}
}

func (r IncidentRecord) ToSyncUpdate() store.SnowSyncUpdate {
	return store.SnowSyncUpdate{
		State:           r.State,
		StateLabel:      r.StateLabel,
		Priority:        r.Priority,
		AssignmentGroup: r.AssignmentGroup,
		ResolvedAt:      r.ResolvedAt,
		ClosedAt:        r.ClosedAt,
	}
}

// Client interacts with the ServiceNow Table API.
type Client struct {
	instanceURL string
	httpClient  *http.Client
	httpTimeout time.Duration
	auth        AuthProvider
	breaker     *CircuitBreaker
	metrics     *observability.Metrics
}

// NewClient constructs a ServiceNow Table API client.
func NewClient(
	instanceURL string,
	httpTimeout time.Duration,
	authProvider AuthProvider,
	breaker *CircuitBreaker,
	metrics *observability.Metrics,
) *Client {
	if httpTimeout <= 0 {
		httpTimeout = 15 * time.Second
	}
	return &Client{
		instanceURL: strings.TrimRight(instanceURL, "/"),
		httpClient:  &http.Client{Timeout: httpTimeout + 5*time.Second},
		httpTimeout: httpTimeout,
		auth:        authProvider,
		breaker:     breaker,
		metrics:     metrics,
	}
}

// CircuitState returns the current circuit breaker state.
func (c *Client) CircuitState() BreakerState {
	if c.breaker == nil {
		return BreakerClosed
	}
	return c.breaker.State()
}

// LookupUserByEmail queries ServiceNow sys_user for an active user matching email (3s timeout).
func (c *Client) LookupUserByEmail(ctx context.Context, email string) (*SysUser, error) {
	q := fmt.Sprintf("email=%s^active=true", strings.TrimSpace(email))
	return c.lookupUser(ctx, q)
}

// LookupUserByUsername queries ServiceNow sys_user for an active user matching user_name (3s timeout).
func (c *Client) LookupUserByUsername(ctx context.Context, username string) (*SysUser, error) {
	q := fmt.Sprintf("user_name=%s^active=true", strings.TrimSpace(username))
	return c.lookupUser(ctx, q)
}

func (c *Client) lookupUser(ctx context.Context, sysparmQuery string) (*SysUser, error) {
	params := url.Values{}
	params.Set("sysparm_query", sysparmQuery)
	params.Set("sysparm_fields", "sys_id,user_name,email")
	params.Set("sysparm_limit", "1")
	endpoint := c.instanceURL + "/api/now/table/sys_user?" + params.Encode()

	lookupCtx, cancel := context.WithTimeout(ctx, userLookupTimeout)
	defer cancel()

	body, err := c.doWithAuthAndRetry(lookupCtx, "user_lookup", http.MethodGet, endpoint, nil, true)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Result []struct {
			SysID    snowField `json:"sys_id"`
			UserName snowField `json:"user_name"`
			Email    snowField `json:"email"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Invalid JSON from ServiceNow sys_user API.").WithCause(err)
	}
	if len(parsed.Result) == 0 || parsed.Result[0].SysID.Val() == "" {
		return nil, nil
	}
	return &SysUser{
		SysID:    parsed.Result[0].SysID.Val(),
		UserName: parsed.Result[0].UserName.Val(),
		Email:    parsed.Result[0].Email.Val(),
	}, nil
}

// FindIncidentByCorrelationID looks up an incident by correlation_id.
func (c *Client) FindIncidentByCorrelationID(ctx context.Context, correlationID string) (*IncidentRecord, error) {
	params := url.Values{}
	params.Set("sysparm_query", "correlation_id="+correlationID)
	params.Set("sysparm_fields", "sys_id,number,state,priority,assignment_group,opened_at,resolved_at,closed_at")
	params.Set("sysparm_display_value", "all")
	params.Set("sysparm_limit", "1")
	return c.findSingleIncident(ctx, "get", c.instanceURL+"/api/now/table/incident?"+params.Encode())
}

// FindIncidentByNumber looks up an incident by number (e.g. INC0012345).
func (c *Client) FindIncidentByNumber(ctx context.Context, number string) (*IncidentRecord, error) {
	params := url.Values{}
	params.Set("sysparm_query", "number="+strings.TrimSpace(number))
	params.Set("sysparm_fields", "sys_id,number,state,priority,assignment_group,opened_at,resolved_at,closed_at")
	params.Set("sysparm_display_value", "all")
	params.Set("sysparm_limit", "1")
	return c.findSingleIncident(ctx, "get", c.instanceURL+"/api/now/table/incident?"+params.Encode())
}

// GetIncidentsBySysIDs batch-fetches incidents for the given sys_ids.
func (c *Client) GetIncidentsBySysIDs(ctx context.Context, operation string, sysIDs []string) ([]IncidentRecord, error) {
	if len(sysIDs) == 0 {
		return nil, nil
	}
	params := url.Values{}
	params.Set("sysparm_query", "sys_idIN"+strings.Join(sysIDs, ","))
	params.Set("sysparm_fields", "sys_id,number,state,priority,assignment_group,opened_at,resolved_at,closed_at,sys_updated_on")
	params.Set("sysparm_display_value", "all")
	endpoint := c.instanceURL + "/api/now/table/incident?" + params.Encode()

	callCtx, cancel := context.WithTimeout(ctx, c.httpTimeout)
	defer cancel()

	body, err := c.doWithAuthAndRetry(callCtx, operation, http.MethodGet, endpoint, nil, true)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Result []rawIncident `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Invalid JSON from ServiceNow incident query.").WithCause(err)
	}

	records := make([]IncidentRecord, 0, len(parsed.Result))
	for _, item := range parsed.Result {
		rec := item.toRecord(c.instanceURL)
		if rec.SysID != "" {
			records = append(records, rec)
		}
	}
	return records, nil
}

// CreateIncident creates a new incident in ServiceNow.
func (c *Client) CreateIncident(ctx context.Context, payload CreateIncidentPayload) (*IncidentRecord, error) {
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Failed to encode ServiceNow incident payload.").WithCause(err)
	}

	params := url.Values{}
	params.Set("sysparm_fields", "sys_id,number,state,impact,urgency,priority,assignment_group,caller_id,opened_at,sys_updated_on")
	params.Set("sysparm_display_value", "all")
	params.Set("sysparm_input_display_value", "true")
	endpoint := c.instanceURL + "/api/now/table/incident?" + params.Encode()

	callCtx, cancel := context.WithTimeout(ctx, c.httpTimeout)
	defer cancel()

	// Never automatically retry POST on 5xx/timeout to avoid duplicate tickets.
	body, err := c.doWithAuthAndRetry(callCtx, "create", http.MethodPost, endpoint, rawPayload, false)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Result rawIncident `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Invalid JSON from ServiceNow create incident response.").WithCause(err)
	}
	rec := parsed.Result.toRecord(c.instanceURL)
	if rec.SysID == "" || rec.Number == "" {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "ServiceNow create response omitted sys_id or number.")
	}
	return &rec, nil
}

// PostWorkNote appends a work note to an existing ServiceNow incident.
func (c *Client) PostWorkNote(ctx context.Context, snowSysID, workNotes string) error {
	rawPayload, err := json.Marshal(map[string]string{"work_notes": workNotes})
	if err != nil {
		return apierror.New(http.StatusBadRequest, apierror.CodeValidationError, "Failed to encode work note payload.").WithCause(err)
	}
	endpoint := fmt.Sprintf("%s/api/now/table/incident/%s", c.instanceURL, url.PathEscape(snowSysID))

	callCtx, cancel := context.WithTimeout(ctx, c.httpTimeout)
	defer cancel()

	_, err = c.doWithAuthAndRetry(callCtx, "patch", http.MethodPatch, endpoint, rawPayload, false)
	return err
}

// ListAssignmentGroups fetches active assignment groups from ServiceNow sys_user_group.
func (c *Client) ListAssignmentGroups(ctx context.Context) ([]model.AssignmentGroupInfo, error) {
	params := url.Values{}
	params.Set("sysparm_query", "active=true")
	params.Set("sysparm_fields", "sys_id,name")
	params.Set("sysparm_limit", "100")
	endpoint := c.instanceURL + "/api/now/table/sys_user_group?" + params.Encode()

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	body, err := c.doWithAuthAndRetry(callCtx, "get", http.MethodGet, endpoint, nil, false)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Result []struct {
			SysID snowField `json:"sys_id"`
			Name  snowField `json:"name"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Invalid JSON from ServiceNow sys_user_group API.").WithCause(err)
	}
	out := make([]model.AssignmentGroupInfo, 0, len(parsed.Result))
	for _, g := range parsed.Result {
		if g.Name.Val() != "" {
			out = append(out, model.AssignmentGroupInfo{
				SysID: g.SysID.Val(),
				Name:  g.Name.DisplayOrVal(),
			})
		}
	}
	return out, nil
}

func (c *Client) findSingleIncident(ctx context.Context, operation, endpoint string) (*IncidentRecord, error) {
	callCtx, cancel := context.WithTimeout(ctx, c.httpTimeout)
	defer cancel()

	body, err := c.doWithAuthAndRetry(callCtx, operation, http.MethodGet, endpoint, nil, true)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Result []rawIncident `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Invalid JSON from ServiceNow incident lookup.").WithCause(err)
	}
	if len(parsed.Result) == 0 {
		return nil, nil
	}
	rec := parsed.Result[0].toRecord(c.instanceURL)
	if rec.SysID == "" {
		return nil, nil
	}
	return &rec, nil
}

func (c *Client) doWithAuthAndRetry(
	ctx context.Context,
	operation string,
	method string,
	endpoint string,
	payload []byte,
	retryTransientGet bool,
) ([]byte, error) {
	if c.breaker != nil && !c.breaker.Allow() {
		c.recordMetric(operation, "circuit_open", 0)
		return nil, apierror.New(http.StatusServiceUnavailable, apierror.CodeSnowUnavailable, "ServiceNow circuit breaker is open; please retry shortly.")
	}

	maxAttempts := 1
	if retryTransientGet && method == http.MethodGet {
		maxAttempts = 2
	}

	tokenRetried := false
	for attempt := 0; attempt < maxAttempts; attempt++ {
		authHeader, rawTok, err := c.auth.AuthorizationHeader(ctx)
		if err != nil {
			if c.breaker != nil {
				c.breaker.RecordFailure()
			}
			c.recordMetric(operation, "auth_error", 0)
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, apierror.New(http.StatusGatewayTimeout, apierror.CodeSnowTimeout, "Timed out obtaining ServiceNow authentication token.").WithCause(err)
			}
			return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Failed to authenticate with ServiceNow.").WithCause(err)
		}

		start := time.Now()
		var bodyReader io.Reader
		if payload != nil {
			bodyReader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
		if err != nil {
			return nil, apierror.New(http.StatusInternalServerError, apierror.CodeInternalError, "Failed to build ServiceNow HTTP request.").WithCause(err)
		}
		req.Header.Set("Authorization", authHeader)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		elapsed := time.Since(start).Seconds()
		if err != nil {
			if c.breaker != nil {
				c.breaker.RecordFailure()
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				c.recordMetric(operation, "timeout", elapsed)
				if attempt+1 < maxAttempts && ctx.Err() == nil {
					continue
				}
				return nil, apierror.New(http.StatusGatewayTimeout, apierror.CodeSnowTimeout, "ServiceNow API request timed out.").WithCause(err)
			}
			c.recordMetric(operation, "transport_error", elapsed)
			if attempt+1 < maxAttempts && ctx.Err() == nil {
				continue
			}
			return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "ServiceNow API request failed.").WithCause(err)
		}

		respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			if c.breaker != nil {
				c.breaker.RecordFailure()
			}
			c.recordMetric(operation, "read_error", elapsed)
			return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "Failed reading ServiceNow response.").WithCause(readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized && c.auth.IsOAuth() && !tokenRetried {
			tokenRetried = true
			c.auth.InvalidateToken(ctx, rawTok)
			c.recordMetric(operation, "401_refresh", elapsed)
			// Retry once immediately without consuming transient GET retry budget.
			attempt--
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if c.breaker != nil {
				c.breaker.RecordSuccess()
			}
			c.recordMetric(operation, strconv.Itoa(resp.StatusCode), elapsed)
			return respBody, nil
		}

		c.recordMetric(operation, strconv.Itoa(resp.StatusCode), elapsed)

		if resp.StatusCode >= 500 {
			if c.breaker != nil {
				c.breaker.RecordFailure()
			}
			if attempt+1 < maxAttempts && ctx.Err() == nil {
				continue
			}
			return nil, &SnowError{
				API:            apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, fmt.Sprintf("ServiceNow returned HTTP %d.", resp.StatusCode)),
				UpstreamStatus: resp.StatusCode,
				UpstreamBody:   string(respBody),
			}
		}

		// 4xx client/validation errors do not trip the circuit breaker (except we still record 429 cleanly).
		if c.breaker != nil {
			c.breaker.RecordSuccess()
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			return nil, &SnowError{
				API:            apierror.New(http.StatusTooManyRequests, apierror.CodeRateLimited, "ServiceNow rate limit exceeded."),
				UpstreamStatus: resp.StatusCode,
				UpstreamBody:   string(respBody),
			}
		}

		if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusForbidden {
			return nil, &SnowError{
				API:            apierror.New(http.StatusUnprocessableEntity, apierror.CodeSnowValidationRejected, fmt.Sprintf("ServiceNow rejected the request (HTTP %d).", resp.StatusCode)),
				UpstreamStatus: resp.StatusCode,
				UpstreamBody:   string(respBody),
			}
		}

		if resp.StatusCode == http.StatusNotFound {
			return nil, &SnowError{
				API:            apierror.New(http.StatusNotFound, apierror.CodeIncidentNotFound, "Requested record was not found in ServiceNow."),
				UpstreamStatus: resp.StatusCode,
				UpstreamBody:   string(respBody),
			}
		}

		return nil, &SnowError{
			API:            apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, fmt.Sprintf("Unexpected ServiceNow HTTP %d response.", resp.StatusCode)),
			UpstreamStatus: resp.StatusCode,
			UpstreamBody:   string(respBody),
		}
	}

	return nil, apierror.New(http.StatusBadGateway, apierror.CodeSnowUpstreamError, "ServiceNow request failed after retries.")
}

func (c *Client) recordMetric(operation, status string, elapsedSec float64) {
	if c.metrics == nil {
		return
	}
	c.metrics.SnowRequestsTotal.WithLabelValues(operation, status).Inc()
	if elapsedSec > 0 {
		c.metrics.SnowRequestDurationSeconds.WithLabelValues(operation).Observe(elapsedSec)
	}
}

// snowField decodes ServiceNow fields returned either as {"value":...,"display_value":...} or plain scalar JSON.
type snowField struct {
	Value        string
	DisplayValue string
}

func (f *snowField) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var obj struct {
			Value        any `json:"value"`
			DisplayValue any `json:"display_value"`
		}
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		f.Value = anyToString(obj.Value)
		f.DisplayValue = anyToString(obj.DisplayValue)
		return nil
	}
	var scalar any
	if err := json.Unmarshal(data, &scalar); err != nil {
		return err
	}
	s := anyToString(scalar)
	f.Value = s
	f.DisplayValue = s
	return nil
}

func (f snowField) Val() string {
	return strings.TrimSpace(f.Value)
}

func (f snowField) DisplayOrVal() string {
	if strings.TrimSpace(f.DisplayValue) != "" {
		return strings.TrimSpace(f.DisplayValue)
	}
	return strings.TrimSpace(f.Value)
}

func anyToString(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

type rawIncident struct {
	SysID           snowField `json:"sys_id"`
	Number          snowField `json:"number"`
	State           snowField `json:"state"`
	Priority        snowField `json:"priority"`
	AssignmentGroup snowField `json:"assignment_group"`
	OpenedAt        snowField `json:"opened_at"`
	ResolvedAt      snowField `json:"resolved_at"`
	ClosedAt        snowField `json:"closed_at"`
}

func (r rawIncident) toRecord(instanceURL string) IncidentRecord {
	sysID := r.SysID.Val()
	stateVal := int16(1)
	if parsed, err := strconv.ParseInt(r.State.Val(), 10, 16); err == nil && model.IsValidSnowState(int16(parsed)) {
		stateVal = int16(parsed)
	}
	label := model.SnowStateLabel(stateVal)

	var priority *int16
	if pStr := r.Priority.Val(); pStr != "" {
		if p, err := strconv.ParseInt(pStr, 10, 16); err == nil && p >= 1 && p <= 5 {
			pv := int16(p)
			priority = &pv
		}
	}

	assignGroup := model.StringPtrOrNil(r.AssignmentGroup.DisplayOrVal())

	var incURL string
	if sysID != "" {
		incURL = fmt.Sprintf("%s/nav_to.do?uri=incident.do?sys_id=%s", strings.TrimRight(instanceURL, "/"), url.QueryEscape(sysID))
	}

	return IncidentRecord{
		SysID:           sysID,
		Number:          r.Number.DisplayOrVal(),
		IncidentURL:     incURL,
		State:           stateVal,
		StateLabel:      label,
		Priority:        priority,
		AssignmentGroup: assignGroup,
		OpenedAt:        parseSnowTimestamp(r.OpenedAt.Val()),
		ResolvedAt:      parseSnowTimestamp(r.ResolvedAt.Val()),
		ClosedAt:        parseSnowTimestamp(r.ClosedAt.Val()),
	}
}

func parseSnowTimestamp(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05Z",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			utc := t.UTC()
			return &utc
		}
	}
	return nil
}
