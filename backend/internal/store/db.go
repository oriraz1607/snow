package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates and verifies a pgxpool.Pool with configured bounds.
func NewPool(ctx context.Context, databaseURL string, maxConns, minConns int32) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolCfg.MaxConns = maxConns
	poolCfg.MinConns = minConns
	poolCfg.HealthCheckPeriod = 30 * time.Second
	poolCfg.ConnConfig.ConnectTimeout = 5 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pgxpool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgresql: %w", err)
	}
	return pool, nil
}

// CheckReadiness verifies PostgreSQL reachability (SELECT 1) and expected schema version within 2s.
func CheckReadiness(ctx context.Context, pool *pgxpool.Pool, expectedSchemaVersion int) error {
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var one int
	if err := pool.QueryRow(checkCtx, `SELECT 1`).Scan(&one); err != nil {
		return fmt.Errorf("postgresql connectivity check failed: %w", err)
	}

	var currentVersion int
	if err := pool.QueryRow(checkCtx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&currentVersion); err != nil {
		return fmt.Errorf("schema_migrations check failed: %w", err)
	}
	if currentVersion < expectedSchemaVersion {
		return fmt.Errorf("schema version %d is behind expected %d", currentVersion, expectedSchemaVersion)
	}
	return nil
}

// WithAdvisoryLock attempts a non-blocking session advisory lock and invokes fn only if acquired.
func WithAdvisoryLock(ctx context.Context, pool *pgxpool.Pool, lockID int64, fn func(context.Context) error) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection for advisory lock: %w", err)
	}
	defer conn.Release()

	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&acquired); err != nil {
		return false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, lockID)
	}()

	return true, fn(ctx)
}
