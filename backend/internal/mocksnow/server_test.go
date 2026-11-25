package mocksnow

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMockServerOAuthIncidentAndReset(t *testing.T) {
	srv := NewServer(Config{
		ClientID:              "test-cid",
		ClientSecret:          "test-csecret",
		DefaultCallerSysID:    "noc_sys_001",
		DefaultCallerUsername: "noc",
	})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Obtain OAuth token
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", "test-cid")
	form.Set("client_secret", "test-csecret")
	resp, err := http.Post(ts.URL+"/oauth_token.do", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("token post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on token endpoint, got %d", resp.StatusCode)
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokResp); err != nil || tokResp.AccessToken == "" {
		t.Fatalf("invalid token response: %v", err)
	}

	// Lookup seeded user alice.smith@corp.example
	uReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/now/table/sys_user?sysparm_query=email=alice.smith@corp.example^active=true", nil)
	uReq.Header.Set("Authorization", "Bearer "+tokResp.AccessToken)
	uResp, err := http.DefaultClient.Do(uReq)
	if err != nil {
		t.Fatalf("sys_user lookup: %v", err)
	}
	defer uResp.Body.Close()
	if uResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on sys_user, got %d", uResp.StatusCode)
	}

	// Create incident
	createPayload := `{"short_description":"Disk Full","impact":"1","urgency":"1","caller_id":"u_alice_001","correlation_id":"alert-1"}`
	cReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/now/table/incident", bytes.NewBufferString(createPayload))
	cReq.Header.Set("Authorization", "Bearer "+tokResp.AccessToken)
	cReq.Header.Set("Content-Type", "application/json")
	cResp, err := http.DefaultClient.Do(cReq)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	defer cResp.Body.Close()
	if cResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 on create incident, got %d", cResp.StatusCode)
	}
}
