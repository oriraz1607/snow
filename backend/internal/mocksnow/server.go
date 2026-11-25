package mocksnow

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

// Config configures the mock ServiceNow server's credentials and default seeded accounts.
type Config struct {
	ClientID              string
	ClientSecret          string
	Username              string
	Password              string
	DefaultCallerSysID    string
	DefaultCallerUsername string
	WebhookURL            string
	WebhookSecret         string
}

// MockUser models a ServiceNow sys_user entry.
type MockUser struct {
	SysID    string `json:"sys_id"`
	UserName string `json:"user_name"`
	Email    string `json:"email"`
	Active   bool   `json:"active"`
}

// MockIncident models a stored ServiceNow incident record for verification and assertions.
type MockIncident struct {
	SysID           string   `json:"sys_id"`
	Number          string   `json:"number"`
	State           int16    `json:"state"`
	StateLabel      string   `json:"state_label"`
	Impact          int16    `json:"impact"`
	Urgency         int16    `json:"urgency"`
	Priority        int16    `json:"priority"`
	ShortDesc       string   `json:"short_description"`
	Description     string   `json:"description"`
	AssignmentGroup string   `json:"assignment_group"`
	Category        string   `json:"category"`
	CallerID        string   `json:"caller_id"`
	CorrelationID   string   `json:"correlation_id"`
	OpenedAt        string   `json:"opened_at"`
	ResolvedAt      string   `json:"resolved_at,omitempty"`
	ClosedAt        string   `json:"closed_at,omitempty"`
	WorkNotes       []string `json:"work_notes"`
}

// FaultConfig allows tests to simulate ServiceNow failures and timeouts deterministically.
type FaultConfig struct {
	CreateTimeoutAfterCommitMs int    `json:"create_timeout_after_commit_ms"`
	CreateStatusCode           int    `json:"create_status_code"`
	CreateErrorBody            string `json:"create_error_body"`
	SysUserDelayMs             int    `json:"sys_user_delay_ms"`
	SysUserStatusCode          int    `json:"sys_user_status_code"`
	Force401Once               bool   `json:"force_401_once"`
	FailAll500Count            int    `json:"fail_all_500_count"`
}

// Server is a thread-safe in-memory mock ServiceNow Table API + OAuth server.
type Server struct {
	cfg Config

	mu        sync.RWMutex
	tokens    map[string]time.Time
	users     map[string]MockUser
	incidents map[string]*MockIncident
	seqNum    int
	faults    FaultConfig
	groups    []model.AssignmentGroupInfo
}

// NewServer constructs and seeds a mock ServiceNow server.
func NewServer(cfg Config) *Server {
	if cfg.DefaultCallerSysID == "" {
		cfg.DefaultCallerSysID = "00000000000000000000000000000noc"
	}
	if cfg.DefaultCallerUsername == "" {
		cfg.DefaultCallerUsername = "noc"
	}
	s := &Server{
		cfg:       cfg,
		tokens:    make(map[string]time.Time),
		users:     make(map[string]MockUser),
		incidents: make(map[string]*MockIncident),
		groups: []model.AssignmentGroupInfo{
			{SysID: "grp_db_sre", Name: "Database SRE"},
			{SysID: "grp_platform", Name: "Platform Engineering"},
			{SysID: "grp_infra", Name: "Infrastructure"},
		},
	}
	s.Reset()
	return s
}

// Reset restores initial seeded users and clears incidents, tokens, and injected faults.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens = make(map[string]time.Time)
	s.incidents = make(map[string]*MockIncident)
	s.seqNum = 0
	s.faults = FaultConfig{}
	s.users = map[string]MockUser{
		"u_alice_001": {
			SysID:    "u_alice_001",
			UserName: "alice.smith",
			Email:    "alice.smith@corp.example",
			Active:   true,
		},
		s.cfg.DefaultCallerSysID: {
			SysID:    s.cfg.DefaultCallerSysID,
			UserName: s.cfg.DefaultCallerUsername,
			Email:    s.cfg.DefaultCallerUsername + "@corp.example",
			Active:   true,
		},
	}
}

// Handler returns the http.Handler serving ServiceNow Table API, OAuth, and /_mock/* controls.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Post("/oauth_token.do", s.handleOAuthToken)

	r.Route("/api/now/table", func(api chi.Router) {
		api.Use(s.authMiddleware)
		api.Get("/sys_user", s.handleGetSysUser)
		api.Get("/sys_user_group", s.handleGetSysUserGroup)
		api.Get("/incident", s.handleGetIncidents)
		api.Post("/incident", s.handleCreateIncident)
		api.Patch("/incident/{sys_id}", s.handlePatchIncident)
	})

	r.Route("/_mock", func(mock chi.Router) {
		mock.Post("/reset", func(w http.ResponseWriter, _ *http.Request) {
			s.Reset()
			writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
		})
		mock.Post("/users", s.handleMockUpsertUser)
		mock.Get("/incidents", s.handleMockListIncidents)
		mock.Post("/incidents/{sys_id}/transition", s.handleMockTransitionIncident)
		mock.Post("/faults", s.handleMockSetFaults)
	})

	return r
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if r.FormValue("grant_type") != "client_credentials" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	if s.cfg.ClientID != "" && (r.FormValue("client_id") != s.cfg.ClientID || r.FormValue("client_secret") != s.cfg.ClientSecret) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}

	tok := randomHex(16)
	s.mu.Lock()
	s.tokens[tok] = time.Now().Add(1 * time.Hour)
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": tok,
		"token_type":   "Bearer",
		"expires_in":   3600,
	})
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.faults.Force401Once {
			s.faults.Force401Once = false
			// Drop existing tokens so OAuth refresh issues a brand-new token.
			s.tokens = make(map[string]time.Time)
			s.mu.Unlock()
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "token_expired"})
			return
		}
		if s.faults.FailAll500Count > 0 {
			s.faults.FailAll500Count--
			s.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "simulated_internal_error"})
			return
		}
		s.mu.Unlock()

		authHdr := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(authHdr, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(authHdr, "Bearer "))
			s.mu.RLock()
			exp, ok := s.tokens[tok]
			s.mu.RUnlock()
			if ok && time.Now().Before(exp) {
				next.ServeHTTP(w, r)
				return
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_token"})
			return
		}

		if strings.HasPrefix(authHdr, "Basic ") && s.cfg.Username != "" {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHdr, "Basic "))
			if err == nil {
				parts := strings.SplitN(string(decoded), ":", 2)
				if len(parts) == 2 && parts[0] == s.cfg.Username && parts[1] == s.cfg.Password {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_basic_credentials"})
			return
		}

		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
	})
}

func (s *Server) handleGetSysUser(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	delay := s.faults.SysUserDelayMs
	statusOverride := s.faults.SysUserStatusCode
	s.mu.RUnlock()

	if delay > 0 {
		select {
		case <-time.After(time.Duration(delay) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	if statusOverride > 0 {
		writeJSON(w, statusOverride, map[string]string{"error": "simulated_sys_user_error"})
		return
	}

	q := r.URL.Query().Get("sysparm_query")
	requireActive := strings.Contains(q, "active=true")

	var targetEmail, targetUser string
	for _, clause := range strings.Split(q, "^") {
		if strings.HasPrefix(clause, "email=") {
			targetEmail = strings.ToLower(strings.TrimPrefix(clause, "email="))
		} else if strings.HasPrefix(clause, "user_name=") {
			targetUser = strings.ToLower(strings.TrimPrefix(clause, "user_name="))
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var matched []map[string]any
	for _, u := range s.users {
		if requireActive && !u.Active {
			continue
		}
		if targetEmail != "" && strings.ToLower(u.Email) != targetEmail {
			continue
		}
		if targetUser != "" && strings.ToLower(u.UserName) != targetUser {
			continue
		}
		matched = append(matched, map[string]any{
			"sys_id":    u.SysID,
			"user_name": u.UserName,
			"email":     u.Email,
		})
		break
	}
	if matched == nil {
		matched = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": matched})
}

func (s *Server) handleGetSysUserGroup(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	res := make([]map[string]any, 0, len(s.groups))
	for _, g := range s.groups {
		res = append(res, map[string]any{
			"sys_id": g.SysID,
			"name":   g.Name,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": res})
}

func (s *Server) handleCreateIncident(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ShortDescription string `json:"short_description"`
		Description      string `json:"description"`
		Impact           string `json:"impact"`
		Urgency          string `json:"urgency"`
		AssignmentGroup  string `json:"assignment_group"`
		Category         string `json:"category"`
		CallerID         string `json:"caller_id"`
		CorrelationID    string `json:"correlation_id"`
		WorkNotes        string `json:"work_notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"message": "invalid json"}})
		return
	}

	s.mu.Lock()
	if s.faults.CreateStatusCode > 0 {
		code := s.faults.CreateStatusCode
		errBody := s.faults.CreateErrorBody
		s.faults.CreateStatusCode = 0
		s.faults.CreateErrorBody = ""
		s.mu.Unlock()
		if errBody == "" {
			errBody = `{"error":{"message":"simulated create error"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(errBody))
		return
	}

	s.seqNum++
	sysID := "snow_" + randomHex(12)
	number := fmt.Sprintf("INC%07d", s.seqNum)
	imp := parseInt16OrDefault(body.Impact, 2)
	urg := parseInt16OrDefault(body.Urgency, 2)
	prio := computePriority(imp, urg)
	openedAt := time.Now().UTC().Format("2006-01-02 15:04:05")

	var notes []string
	if strings.TrimSpace(body.WorkNotes) != "" {
		notes = append(notes, body.WorkNotes)
	}

	inc := &MockIncident{
		SysID:           sysID,
		Number:          number,
		State:           1,
		StateLabel:      model.SnowStateLabel(1),
		Impact:          imp,
		Urgency:         urg,
		Priority:        prio,
		ShortDesc:       body.ShortDescription,
		Description:     body.Description,
		AssignmentGroup: body.AssignmentGroup,
		Category:        body.Category,
		CallerID:        body.CallerID,
		CorrelationID:   body.CorrelationID,
		OpenedAt:        openedAt,
		WorkNotes:       notes,
	}
	s.incidents[sysID] = inc

	delayAfterCommit := s.faults.CreateTimeoutAfterCommitMs
	s.faults.CreateTimeoutAfterCommitMs = 0
	s.mu.Unlock()

	if delayAfterCommit > 0 {
		select {
		case <-time.After(time.Duration(delayAfterCommit) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{"result": formatIncidentDisplayAll(inc)})
}

func (s *Server) handleGetIncidents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("sysparm_query")

	s.mu.RLock()
	defer s.mu.RUnlock()

	var matched []map[string]any
	switch {
	case strings.HasPrefix(q, "correlation_id="):
		cid := strings.TrimPrefix(q, "correlation_id=")
		for _, inc := range s.incidents {
			if inc.CorrelationID == cid {
				matched = append(matched, formatIncidentDisplayAll(inc))
				break
			}
		}
	case strings.HasPrefix(q, "number="):
		num := strings.TrimPrefix(q, "number=")
		for _, inc := range s.incidents {
			if strings.EqualFold(inc.Number, num) {
				matched = append(matched, formatIncidentDisplayAll(inc))
				break
			}
		}
	case strings.HasPrefix(q, "sys_idIN"):
		rawIDs := strings.Split(strings.TrimPrefix(q, "sys_idIN"), ",")
		wanted := make(map[string]bool, len(rawIDs))
		for _, id := range rawIDs {
			wanted[strings.TrimSpace(id)] = true
		}
		for _, inc := range s.incidents {
			if wanted[inc.SysID] {
				matched = append(matched, formatIncidentDisplayAll(inc))
			}
		}
	default:
		for _, inc := range s.incidents {
			matched = append(matched, formatIncidentDisplayAll(inc))
		}
	}

	if matched == nil {
		matched = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"result": matched})
}

func (s *Server) handlePatchIncident(w http.ResponseWriter, r *http.Request) {
	sysID := chi.URLParam(r, "sys_id")
	var body struct {
		State           *int16  `json:"state"`
		AssignmentGroup *string `json:"assignment_group"`
		WorkNotes       *string `json:"work_notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	inc, ok := s.incidents[sysID]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	if body.State != nil && model.IsValidSnowState(*body.State) {
		inc.State = *body.State
		inc.StateLabel = model.SnowStateLabel(*body.State)
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		if *body.State == 6 {
			inc.ResolvedAt = now
		} else if *body.State == 7 || *body.State == 8 {
			inc.ClosedAt = now
		}
	}
	if body.AssignmentGroup != nil {
		inc.AssignmentGroup = *body.AssignmentGroup
	}
	if body.WorkNotes != nil && strings.TrimSpace(*body.WorkNotes) != "" {
		inc.WorkNotes = append(inc.WorkNotes, *body.WorkNotes)
	}

	writeJSON(w, http.StatusOK, map[string]any{"result": formatIncidentDisplayAll(inc)})
}

func (s *Server) handleMockUpsertUser(w http.ResponseWriter, r *http.Request) {
	var u MockUser
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil || u.SysID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sys_id is required"})
		return
	}
	s.mu.Lock()
	s.users[u.SysID] = u
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleMockListIncidents(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]MockIncident, 0, len(s.incidents))
	for _, inc := range s.incidents {
		copyInc := *inc
		copyInc.WorkNotes = append([]string(nil), inc.WorkNotes...)
		out = append(out, copyInc)
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": out})
}

func (s *Server) handleMockTransitionIncident(w http.ResponseWriter, r *http.Request) {
	sysID := chi.URLParam(r, "sys_id")
	var req struct {
		State           int16   `json:"state"`
		AssignmentGroup *string `json:"assignment_group"`
		UpdatedBy       string  `json:"updated_by"`
		CloseNotes      string  `json:"close_notes"`
		SendWebhook     bool    `json:"send_webhook"`
		WebhookURL      string  `json:"webhook_url"`
		WebhookSecret   string  `json:"webhook_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	if !model.IsValidSnowState(req.State) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid state"})
		return
	}

	s.mu.Lock()
	inc, ok := s.incidents[sysID]
	if !ok {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "incident not found"})
		return
	}
	inc.State = req.State
	inc.StateLabel = model.SnowStateLabel(req.State)
	if req.AssignmentGroup != nil {
		inc.AssignmentGroup = *req.AssignmentGroup
	}
	nowRFC := time.Now().UTC().Format(time.RFC3339)
	nowSnow := time.Now().UTC().Format("2006-01-02 15:04:05")
	var resolvedPtr, closedPtr *string
	if req.State == 6 {
		inc.ResolvedAt = nowSnow
		resolvedPtr = &nowRFC
	} else if req.State == 7 || req.State == 8 {
		inc.ClosedAt = nowSnow
		closedPtr = &nowRFC
	}
	snapshot := *inc
	s.mu.Unlock()

	if req.SendWebhook {
		targetURL := strings.TrimSpace(req.WebhookURL)
		if targetURL == "" {
			targetURL = s.cfg.WebhookURL
		}
		secret := req.WebhookSecret
		if secret == "" {
			secret = s.cfg.WebhookSecret
		}
		if _, err := url.ParseRequestURI(targetURL); err != nil || secret == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "webhook_url and webhook_secret must be configured"})
			return
		}
		updatedBy := req.UpdatedBy
		if updatedBy == "" {
			updatedBy = "mock.resolver"
		}
		payload := model.ServiceNowWebhookPayload{
			SysID:           snapshot.SysID,
			Number:          snapshot.Number,
			CorrelationID:   snapshot.CorrelationID,
			State:           snapshot.State,
			StateLabel:      snapshot.StateLabel,
			Priority:        &snapshot.Priority,
			AssignmentGroup: model.StringPtrOrNil(snapshot.AssignmentGroup),
			UpdatedBy:       updatedBy,
			ResolvedAt:      resolvedPtr,
			ClosedAt:        closedPtr,
			CloseNotes:      req.CloseNotes,
		}
		raw, _ := json.Marshal(payload)
		whReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(raw))
		whReq.Header.Set("Content-Type", "application/json")
		whReq.Header.Set("X-SNOW-Signature-256", auth.ComputeHMACSHA256Header(raw, secret))

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(whReq)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed delivering inbound webhook: " + err.Error()})
			return
		}
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("webhook target returned %d: %s", resp.StatusCode, string(respBody))})
			return
		}
	}

	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleMockSetFaults(w http.ResponseWriter, r *http.Request) {
	var f FaultConfig
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	s.mu.Lock()
	s.faults = f
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, f)
}

func formatIncidentDisplayAll(inc *MockIncident) map[string]any {
	res := map[string]any{
		"sys_id":            disp(inc.SysID, inc.SysID),
		"number":            disp(inc.Number, inc.Number),
		"state":             disp(strconv.Itoa(int(inc.State)), inc.StateLabel),
		"impact":            disp(strconv.Itoa(int(inc.Impact)), strconv.Itoa(int(inc.Impact))),
		"urgency":           disp(strconv.Itoa(int(inc.Urgency)), strconv.Itoa(int(inc.Urgency))),
		"priority":          disp(strconv.Itoa(int(inc.Priority)), fmt.Sprintf("%d", inc.Priority)),
		"assignment_group":  disp(inc.AssignmentGroup, inc.AssignmentGroup),
		"caller_id":         disp(inc.CallerID, inc.CallerID),
		"opened_at":         disp(inc.OpenedAt, inc.OpenedAt),
		"correlation_id":    disp(inc.CorrelationID, inc.CorrelationID),
		"short_description": disp(inc.ShortDesc, inc.ShortDesc),
	}
	if inc.ResolvedAt != "" {
		res["resolved_at"] = disp(inc.ResolvedAt, inc.ResolvedAt)
	}
	if inc.ClosedAt != "" {
		res["closed_at"] = disp(inc.ClosedAt, inc.ClosedAt)
	}
	return res
}

func disp(val, display string) map[string]string {
	return map[string]string{
		"value":         val,
		"display_value": display,
	}
}

func parseInt16OrDefault(s string, def int16) int16 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 16)
	if err != nil || v < 1 || v > 3 {
		return def
	}
	return int16(v)
}

func computePriority(impact, urgency int16) int16 {
	p := impact + urgency - 1
	if p < 1 {
		return 1
	}
	if p > 5 {
		return 5
	}
	return p
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
