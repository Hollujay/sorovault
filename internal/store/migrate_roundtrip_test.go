package store_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
)

// TestMigrationRoundtrip migrates up fully, down to zero, and up again,
// asserting the schema matches the first up migration.
// It runs when TEST_DATABASE_URL is set, like the other store tests.
func TestMigrationRoundtrip(t *testing.T) {
	baseURL := os.Getenv(TestDatabaseURLEnv)
	if baseURL == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}

	ctx := context.Background()
	dbURL := isolatedDatabase(t, ctx, baseURL)

	// First migration up
	require.NoError(t, store.Migrate(dbURL))

	// Migrate down to zero
	require.NoError(t, store.MigrateDown(dbURL))

	// Verify schema is back to zero
	current, _, dirty, err := store.MigrationStatus(ctx, dbURL)
	require.NoError(t, err)
	assert.False(t, dirty, "database should be at version zero after down migration")
	assert.Equal(t, uint(0), current, "database version should be zero")

	// Migrate up again
	require.NoError(t, store.Migrate(dbURL))

	// Verify schema matches the first up migration
	current, latest, dirty, err := store.MigrationStatus(ctx, dbURL)
	require.NoError(t, err)
	assert.False(t, dirty, "database should not be dirty after second up migration")
	assert.Equal(t, latest, current, "database should be at latest migration version")

	// Verify columns that existed after the first up migration are present
	// after the second up migration.
	checkColumnAfterRoundtrip(t, ctx, dbURL, "specs", "symbols")
	checkColumnAfterRoundtrip(t, ctx, dbURL, "contracts", "source_url")
	checkColumnAfterRoundtrip(t, ctx, dbURL, "contracts", "source_commit")
}

// isolatedDatabase creates an empty database next to the one baseURL names
// and returns a URL for it. Migrating all the way down drops every table, and
// other packages' tests use the shared database at the same time, so the round
// trip must not run there.
func isolatedDatabase(t *testing.T, ctx context.Context, baseURL string) string {
	t.Helper()

	conn, err := pgx.Connect(ctx, baseURL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	name := fmt.Sprintf("sorovault_roundtrip_%d", time.Now().UnixNano())
	_, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	})

	u, err := url.Parse(baseURL)
	require.NoError(t, err)
	u.Path = "/" + name
	return u.String()
}

func checkColumnAfterRoundtrip(t *testing.T, ctx context.Context, dbURL, table, column string) {
	t.Helper()
	pool, err := pgxpool.New(ctx, dbURL)
	require.NoError(t, err)
	defer pool.Close()

	var columnName string
	err = pool.QueryRow(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_name = $1 AND table_schema = 'public' AND column_name = $2`,
		table, column).Scan(&columnName)
	assert.NoError(t, err, "column %s.%s should exist after round-trip", table, column)
}
