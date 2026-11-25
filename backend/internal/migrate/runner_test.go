package migrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigrationsOrderingAndValidation(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"0002_add_idx.up.sql":      "CREATE INDEX idx_b ON a(b);",
		"0002_add_idx.down.sql":    "DROP INDEX idx_b;",
		"0001_init.up.sql":         "CREATE TABLE a (b INT);",
		"0001_init.down.sql":       "DROP TABLE a;",
		"README.txt":               "ignored",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	pairs, err := LoadMigrations(dir)
	if err != nil {
		t.Fatalf("LoadMigrations error: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 migration pairs, got %d", len(pairs))
	}
	if pairs[0].Version != 1 || pairs[0].Name != "init" || pairs[1].Version != 2 || pairs[1].Name != "add_idx" {
		t.Fatalf("unexpected migration ordering: %+v", pairs)
	}
}

func TestLoadMigrationsFromRepoDirectory(t *testing.T) {
	pairs, err := LoadMigrations("../../../migrations")
	if err != nil {
		t.Fatalf("LoadMigrations from repo migrations dir failed: %v", err)
	}
	if len(pairs) != ExpectedSchemaVersion {
		t.Fatalf("expected %d migration(s), got %d", ExpectedSchemaVersion, len(pairs))
	}
	if pairs[0].Version != 1 || pairs[0].DownSQL == "" {
		t.Fatalf("unexpected initial migration pair: %+v", pairs[0])
	}
}
