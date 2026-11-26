package servicenow

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oriraz/grafana-snow-integration/backend/internal/auth"
	"github.com/oriraz/grafana-snow-integration/backend/internal/model"
	"github.com/oriraz/grafana-snow-integration/backend/internal/observability"
	"github.com/oriraz/grafana-snow-integration/backend/internal/store"
)

const (
	positiveUserCacheTTL = 6 * time.Hour
	negativeUserCacheTTL = 5 * time.Minute
)

// UserCacheStore abstracts PostgreSQL snow_user_cache persistence for caller resolution.
type UserCacheStore interface {
	LookupUserCache(ctx context.Context, cacheKey string) (*model.SnowUserCacheEntry, error)
	UpsertUserCache(ctx context.Context, entry model.SnowUserCacheEntry) error
}

// UserLookupClient abstracts ServiceNow sys_user lookups.
type UserLookupClient interface {
	LookupUserByEmail(ctx context.Context, email string) (*SysUser, error)
	LookupUserByUsername(ctx context.Context, username string) (*SysUser, error)
}

// CallerResolver resolves an operator's ServiceNow caller_id using SSO lookup + snow_user_cache
// with automatic fallback to SNOW_DEFAULT_NOC_CALLER_SYS_ID.
type CallerResolver struct {
	cache              UserCacheStore
	client             UserLookupClient
	defaultCallerSysID string
	metrics            *observability.Metrics
	logger             *slog.Logger
}

// NewCallerResolver constructs a CallerResolver.
func NewCallerResolver(
	cache UserCacheStore,
	client UserLookupClient,
	defaultCallerSysID string,
	metrics *observability.Metrics,
	logger *slog.Logger,
) *CallerResolver {
	return &CallerResolver{
		cache:              cache,
		client:             client,
		defaultCallerSysID: defaultCallerSysID,
		metrics:            metrics,
		logger:             logger,
	}
}

// Resolve determines caller_sys_id, caller_resolution_status, and optional fallback reason.
// In sso_user mode, email is attempted first; if empty or unmatched, it falls back to
// user_name (op.Login) before falling back to SNOW_DEFAULT_NOC_CALLER_SYS_ID.
func (r *CallerResolver) Resolve(ctx context.Context, mode model.SnowCallerMode, op auth.OperatorContext) store.CallerResolution {
	if mode == model.CallerModeGenericNOC {
		r.recordCallerMetric(string(model.CallerResGenericNOCSelected))
		return store.CallerResolution{
			SysID:            r.defaultCallerSysID,
			ResolutionStatus: model.CallerResGenericNOCSelected,
		}
	}

	email := strings.ToLower(strings.TrimSpace(op.Email))
	login := strings.ToLower(strings.TrimSpace(op.Login))

	if email == "" && login == "" {
		reason := "Grafana session has no email address or login; fell back to generic service user."
		r.recordCallerMetric(string(model.CallerResFallbackNOC))
		return store.CallerResolution{
			SysID:            r.defaultCallerSysID,
			ResolutionStatus: model.CallerResFallbackNOC,
			FallbackReason:   &reason,
		}
	}

	// Step 1: Lookup by email if non-empty.
	if email != "" {
		res, matched, transientErr := r.resolveOne(ctx, email, email, false)
		if matched || transientErr {
			return res
		}
	}

	// Step 2: Email empty or definite miss -> fallback to ServiceNow user_name (Grafana login).
	if login != "" {
		res, _, _ := r.resolveOne(ctx, "user:"+login, login, true)
		return res
	}

	reason := fmt.Sprintf("No active sys_user found for %s; fell back to generic service user.", email)
	r.recordCallerMetric(string(model.CallerResFallbackNOC))
	return store.CallerResolution{
		SysID:            r.defaultCallerSysID,
		ResolutionStatus: model.CallerResFallbackNOC,
		FallbackReason:   &reason,
	}
}

func (r *CallerResolver) resolveOne(
	ctx context.Context,
	cacheKey string,
	lookupKey string,
	byUsername bool,
) (res store.CallerResolution, matched bool, transientErr bool) {
	if r.cache != nil {
		cached, err := r.cache.LookupUserCache(ctx, cacheKey)
		if err != nil && r.logger != nil {
			r.logger.Warn("snow_user_cache lookup failed; continuing with live lookup",
				slog.String("component", "caller_resolver"),
				slog.String("error", err.Error()),
			)
		} else if cached != nil {
			if cached.IsActive && cached.SnowSysID != nil && *cached.SnowSysID != "" {
				r.recordCallerMetric(string(model.CallerResSSOMatched))
				return store.CallerResolution{
					SysID:            *cached.SnowSysID,
					ResolutionStatus: model.CallerResSSOMatched,
				}, true, false
			}
			reason := fmt.Sprintf("Cached negative ServiceNow sys_user lookup for %s; fell back to generic service user.", lookupKey)
			r.recordCallerMetric(string(model.CallerResFallbackNOC))
			return store.CallerResolution{
				SysID:            r.defaultCallerSysID,
				ResolutionStatus: model.CallerResFallbackNOC,
				FallbackReason:   &reason,
			}, false, false
		}
	}

	var user *SysUser
	var err error
	if byUsername {
		user, err = r.client.LookupUserByUsername(ctx, lookupKey)
	} else {
		user, err = r.client.LookupUserByEmail(ctx, lookupKey)
	}

	if err != nil {
		// Transient HTTP errors or timeouts are NOT cached so future requests can recover immediately.
		if r.logger != nil {
			r.logger.Warn("servicenow sys_user lookup failed; falling back to default caller",
				slog.String("component", "caller_resolver"),
				slog.String("lookup_key", lookupKey),
				slog.String("error", err.Error()),
			)
		}
		reason := fmt.Sprintf("ServiceNow sys_user lookup failed or timed out for %s; fell back to generic service user.", lookupKey)
		r.recordCallerMetric(string(model.CallerResFallbackNOC))
		return store.CallerResolution{
			SysID:            r.defaultCallerSysID,
			ResolutionStatus: model.CallerResFallbackNOC,
			FallbackReason:   &reason,
		}, false, true
	}

	if user != nil && user.SysID != "" {
		if r.cache != nil {
			sysID := user.SysID
			uname := user.UserName
			_ = r.cache.UpsertUserCache(ctx, model.SnowUserCacheEntry{
				Email:        cacheKey,
				SnowSysID:    &sysID,
				SnowUserName: model.StringPtrOrNil(uname),
				IsActive:     true,
				ExpiresAt:    time.Now().UTC().Add(positiveUserCacheTTL),
			})
		}
		r.recordCallerMetric(string(model.CallerResSSOMatched))
		return store.CallerResolution{
			SysID:            user.SysID,
			ResolutionStatus: model.CallerResSSOMatched,
		}, true, false
	}

	// Definite miss (200 OK with empty result): cache negatively for 5m.
	if r.cache != nil {
		_ = r.cache.UpsertUserCache(ctx, model.SnowUserCacheEntry{
			Email:     cacheKey,
			SnowSysID: nil,
			IsActive:  false,
			ExpiresAt: time.Now().UTC().Add(negativeUserCacheTTL),
		})
	}
	reason := fmt.Sprintf("No active sys_user found for %s; fell back to generic service user.", lookupKey)
	r.recordCallerMetric(string(model.CallerResFallbackNOC))
	return store.CallerResolution{
		SysID:            r.defaultCallerSysID,
		ResolutionStatus: model.CallerResFallbackNOC,
		FallbackReason:   &reason,
	}, false, false
}

func (r *CallerResolver) recordCallerMetric(status string) {
	if r.metrics != nil {
		r.metrics.SnowCallerResolutionTotal.WithLabelValues(status).Inc()
	}
}
