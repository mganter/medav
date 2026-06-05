package storage

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockKey is an arbitrary constant used to serialise migrations across
// concurrently starting instances.
const advisoryLockKey = 0x6d65646176 // "medav"

// Migrate applies every embedded migration whose numeric prefix is greater than
// the highest already-applied version, each in its own transaction, guarded by
// a session-level advisory lock so concurrent instances don't race.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", advisoryLockKey)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version    integer     NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	var current int
	if err := conn.QueryRow(ctx, "SELECT COALESCE(max(version), 0) FROM schema_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema_version: %w", err)
	}

	files, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range files {
		if m.version <= current {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_version (version) VALUES ($1)", m.version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %d: %w", m.version, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded *.sql files and returns them ordered by the
// integer that prefixes each filename (e.g. "0001_init.sql" -> version 1).
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		numStr, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must be named <version>_<desc>.sql", e.Name())
		}
		version, err := strconv.Atoi(numStr)
		if err != nil {
			return nil, fmt.Errorf("migration %q has non-numeric version prefix: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: e.Name(), sql: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
