package service

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

type stubConfluenceStore struct {
	lastAlertID string
	lastOp      auth.OperatorContext
	lastURL     string
	calls       int
	ret         *model.Alert
	err         error
}

func (s *stubConfluenceStore) UpsertAlertConfluenceURL(_ context.Context, alertID string, op auth.OperatorContext, confluenceURL string) (*model.Alert, error) {
	s.calls++
	s.lastAlertID = alertID
	s.lastOp = op
	s.lastURL = confluenceURL
	return s.ret, s.err
}

func TestConfluenceURLValidationAndService(t *testing.T) {
	t.Run("rejects non-https, embedded creds, whitespace, and disallowed host", func(t *testing.T) {
		store := &stubConfluenceStore{}
		svc := NewConfluenceService(store, []string{"confluence.corp.example", ".atlassian.net"})
		op := auth.OperatorContext{Login: "alice", Role: "Editor"}

		invalid := []string{
			"",
			"   ",
			"http://confluence.corp.example/display/NOC/Runbook",
			"javascript:alert(1)",
			"data:text/html,<script>alert(1)</script>",
			"https://user:pass@confluence.corp.example/page",
			"https://confluence.corp.example/page with spaces",
			"https://evil.example/display/NOC/Runbook",
			"https://notatlassian.net/wiki",
		}
		for _, bad := range invalid {
			_, err := svc.UpdateAlertConfluenceURL(context.Background(), "11111111-1111-4111-8111-111111111111", op, bad)
			var apiErr *apierror.APIError
			if err == nil || !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest || apiErr.Code != apierror.CodeValidationError {
				t.Fatalf("expected 400 VALIDATION_ERROR for %q, got %v", bad, err)
			}
		}
		if store.calls != 0 {
			t.Fatalf("expected 0 store calls on invalid URLs, got %d", store.calls)
		}
	})

	t.Run("accepts and normalizes valid https URL matching allowlist", func(t *testing.T) {
		expectedURL := "https://team.atlassian.net/wiki/spaces/NOC/pages/42"
		store := &stubConfluenceStore{
			ret: &model.Alert{
				ID:                  "11111111-1111-4111-8111-111111111111",
				ConfluenceURL:       &expectedURL,
				ConfluenceURLSource: model.ConfluenceSourceOverride,
			},
		}
		svc := NewConfluenceService(store, []string{"confluence.corp.example", ".atlassian.net"})
		op := auth.OperatorContext{Login: "alice", Email: "alice@corp.example", Role: "Editor"}

		got, err := svc.UpdateAlertConfluenceURL(context.Background(), "11111111-1111-4111-8111-111111111111", op, "  HTTPS://team.atlassian.net/wiki/spaces/NOC/pages/42  ")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if store.calls != 1 || store.lastURL != expectedURL || got.ConfluenceURLSource != model.ConfluenceSourceOverride {
			t.Fatalf("unexpected store invocation or result: calls=%d url=%q got=%+v", store.calls, store.lastURL, got)
		}
	})

	t.Run("ResolveConfluenceURL follows priority noc_override > annotation > runbook_url", func(t *testing.T) {
		override := "https://confluence.corp.example/override"
		ann := map[string]string{
			"confluence_url": "https://confluence.corp.example/ann",
			"runbook_url":    "https://confluence.corp.example/runbook",
		}
		u, src := model.ResolveConfluenceURL(&override, ann, nil)
		if u == nil || *u != override || src != model.ConfluenceSourceOverride {
			t.Fatalf("expected override priority, got %v (%s)", u, src)
		}

		u, src = model.ResolveConfluenceURL(nil, ann, nil)
		if u == nil || *u != ann["confluence_url"] || src != model.ConfluenceSourceAnnotation {
			t.Fatalf("expected annotation priority, got %v (%s)", u, src)
		}

		u, src = model.ResolveConfluenceURL(nil, map[string]string{
			"confluence_url": "http://insecure.example/page",
			"runbook_url":    "https://confluence.corp.example/runbook",
		}, nil)
		if u == nil || *u != "https://confluence.corp.example/runbook" || src != model.ConfluenceSourceRunbook {
			t.Fatalf("expected valid https runbook_url fallback, got %v (%s)", u, src)
		}

		u, src = model.ResolveConfluenceURL(nil, map[string]string{"runbook_url": "http://insecure"}, nil)
		if u != nil || src != model.ConfluenceSourceNone {
			t.Fatalf("expected none for insecure URL, got %v (%s)", u, src)
		}
	})
}
