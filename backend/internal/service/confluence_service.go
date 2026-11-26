package service

import (
	"context"
	"net/http"

	"github.com/oriraz/grafana-snow-integration/backend/internal/apierror"
	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
)

// ConfluenceStore abstracts PostgreSQL persistence for NOC Confluence documentation links.
type ConfluenceStore interface {
	UpsertAlertConfluenceURL(ctx context.Context, alertID string, op auth.OperatorContext, confluenceURL string) (*model.Alert, error)
}

// ConfluenceService validates and persists NOC Confluence documentation links per alert_name.
type ConfluenceService struct {
	store        ConfluenceStore
	allowedHosts []string
}

// NewConfluenceService constructs a ConfluenceService.
func NewConfluenceService(store ConfluenceStore, allowedHosts []string) *ConfluenceService {
	copied := make([]string, len(allowedHosts))
	copy(copied, allowedHosts)
	return &ConfluenceService{
		store:        store,
		allowedHosts: copied,
	}
}

// UpdateAlertConfluenceURL validates rawURL against HTTPS / host policy and persists it with audit history.
func (s *ConfluenceService) UpdateAlertConfluenceURL(
	ctx context.Context,
	alertID string,
	op auth.OperatorContext,
	rawURL string,
) (*model.Alert, error) {
	validatedURL, err := model.ValidateConfluenceURL(rawURL, s.allowedHosts)
	if err != nil {
		return nil, apierror.New(http.StatusBadRequest, apierror.CodeValidationError, err.Error())
	}
	return s.store.UpsertAlertConfluenceURL(ctx, alertID, op, validatedURL)
}
