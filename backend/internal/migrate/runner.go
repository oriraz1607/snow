package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ExpectedSchemaVersion is verified by /readyz against schema_migrations.
const ExpectedSchemaVersion = 7

const migrationAdvisoryLockID int64 = 728394015

var (
	migrationFilePattern = regexp.MustCompile(`^(\d+)_([a-zA-Z0-9_]+)\.(up|down)\.sql$`)
	validRolePattern     = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// MigrationPair represents a paired up/down SQL migration version.
type MigrationPair struct {
	Version int
	Name    string
	UpSQL   string
	DownSQL string
}

// MigrationStatus reports the applied status of one migration version.
type MigrationStatus struct {
	Version int
	Name    string
	Applied bool
}

// LoadMigrations reads and validates paired .up.sql / .down.sql files from dir.
func LoadMigrations(dir string) ([]MigrationPair, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}

	byVersion := make(map[int]*MigrationPair)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("invalid migration version in %q", entry.Name())
		}
		name := matches[2]
		direction := matches[3]

		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration file %q: %w", entry.Name(), err)
		}

		pair, ok := byVersion[version]
		if !ok {
			pair = &MigrationPair{Version: version, Name: name}
			byVersion[version] = pair
		} else if pair.Name != name {
			return nil, fmt.Errorf("migration %d name mismatch: %q vs %q", version, pair.Name, name)
		}

		if direction == "up" {
			pair.UpSQL = string(content)
		} else {
			pair.DownSQL = string(content)
		}
	}

	versions := make([]int, 0, len(byVersion))
	for v, p := range byVersion {
		if strings.TrimSpace(p.UpSQL) == "" {
			return nil, fmt.Errorf("migration %d (%s) is missing .up.sql content", v, p.Name)
		}
		versions = append(versions, v)
	}
	sort.Ints(versions)

	out := make([]MigrationPair, 0, len(versions))
	for _, v := range versions {
		out = append(out, *byVersion[v])
	}
	return out, nil
}

// Runner applies and rolls back SQL migrations using a PostgreSQL advisory lock.
type Runner struct {
	conn       *pgx.Conn
	migrations []MigrationPair
}

// NewRunner creates a Runner from an open connection and migrations directory.
func NewRunner(conn *pgx.Conn, migrationsDir string) (*Runner, error) {
	pairs, err := LoadMigrations(migrationsDir)
	if err != nil {
		return nil, err
	}
	return &Runner{
		conn:       conn,
		migrations: pairs,
	}, nil
}

func (r *Runner) withLock(ctx context.Context, fn func(context.Context) error) error {
	if _, err := r.conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationAdvisoryLockID); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		_, _ = r.conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationAdvisoryLockID)
	}()

	if _, err := r.conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}

	return fn(ctx)
}

func (r *Runner) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := r.conn.Query(ctx, `SELECT version FROM schema_migrations ORDER BY version ASC`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// Up applies all pending migrations in ascending order. Returns the number applied.
func (r *Runner) Up(ctx context.Context) (int, error) {
	count := 0
	err := r.withLock(ctx, func(ctx context.Context) error {
		applied, err := r.appliedVersions(ctx)
		if err != nil {
			return err
		}

		for _, m := range r.migrations {
			if applied[m.Version] {
				continue
			}

			tx, err := r.conn.Begin(ctx)
			if err != nil {
				return fmt.Errorf("begin migration %d: %w", m.Version, err)
			}
			if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("execute migration %d (%s) up: %w", m.Version, m.Name, err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`, m.Version, m.Name); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("record migration %d: %w", m.Version, err)
			}
			if err := tx.Commit(ctx); err != nil {
				return fmt.Errorf("commit migration %d: %w", m.Version, err)
			}
			count++
		}
		return nil
	})
	return count, err
}

// Down rolls back the single highest applied migration. Returns true if one was rolled back.
func (r *Runner) Down(ctx context.Context) (bool, error) {
	rolledBack := false
	err := r.withLock(ctx, func(ctx context.Context) error {
		applied, err := r.appliedVersions(ctx)
		if err != nil {
			return err
		}

		var latest *MigrationPair
		for i := len(r.migrations) - 1; i >= 0; i-- {
			if applied[r.migrations[i].Version] {
				m := r.migrations[i]
				latest = &m
				break
			}
		}
		if latest == nil {
			return nil
		}
		if strings.TrimSpace(latest.DownSQL) == "" {
			return fmt.Errorf("migration %d (%s) has no .down.sql", latest.Version, latest.Name)
		}

		tx, err := r.conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin down migration %d: %w", latest.Version, err)
		}
		if _, err := tx.Exec(ctx, latest.DownSQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("execute migration %d (%s) down: %w", latest.Version, latest.Name, err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, latest.Version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("delete migration %d record: %w", latest.Version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit down migration %d: %w", latest.Version, err)
		}
		rolledBack = true
		return nil
	})
	return rolledBack, err
}

// Status returns the ordered list of migrations and whether each is applied.
func (r *Runner) Status(ctx context.Context) ([]MigrationStatus, error) {
	var result []MigrationStatus
	err := r.withLock(ctx, func(ctx context.Context) error {
		applied, err := r.appliedVersions(ctx)
		if err != nil {
			return err
		}
		result = make([]MigrationStatus, 0, len(r.migrations))
		for _, m := range r.migrations {
			result = append(result, MigrationStatus{
				Version: m.Version,
				Name:    m.Name,
				Applied: applied[m.Version],
			})
		}
		return nil
	})
	return result, err
}

// GrantAppRole configures least-privilege permissions for the runtime application role:
// SELECT/INSERT only on alert_audit_log (no UPDATE/DELETE) and full DML on operational tables.
func GrantAppRole(ctx context.Context, conn *pgx.Conn, roleName string) error {
	roleName = strings.TrimSpace(roleName)
	if roleName == "" {
		return nil
	}
	if !validRolePattern.MatchString(roleName) {
		return fmt.Errorf("invalid PostgreSQL role name %q", roleName)
	}

	var currentUser string
	if err := conn.QueryRow(ctx, `SELECT current_user`).Scan(&currentUser); err != nil {
		return fmt.Errorf("query current_user: %w", err)
	}
	if currentUser == roleName {
		return nil
	}

	ident := pgx.Identifier{roleName}.Sanitize()
	statements := []string{
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, ident),
		fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON alerts, servicenow_incidents, snow_user_cache, webhook_ingestion_log, alert_fingerprint_teams, idempotency_keys, team_assignment_group_mappings, alert_confluence_docs TO %s`, ident),
		fmt.Sprintf(`GRANT SELECT, INSERT ON alert_audit_log TO %s`, ident),
		fmt.Sprintf(`REVOKE UPDATE, DELETE ON alert_audit_log FROM %s`, ident),
		fmt.Sprintf(`GRANT SELECT ON schema_migrations TO %s`, ident),
		fmt.Sprintf(`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s`, ident),
	}
	for _, stmt := range statements {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply app role privilege (%s): %w", stmt, err)
		}
	}
	return nil
}
