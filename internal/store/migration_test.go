package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
)

// TestLatestMigrationVersion proves the embedded migrations can be counted
// without a database, which is what the doctor command compares against.
func TestLatestMigrationVersion(t *testing.T) {
	t.Parallel()

	latest, err := store.LatestMigrationVersion()
	require.NoError(t, err)
	assert.Positive(t, latest, "the repository ships migrations")
}

// TestEmbeddedMigrations proves the embedded migrations can be listed by name
// without a database, which is what "migrate status" reports as pending.
func TestEmbeddedMigrations(t *testing.T) {
	t.Parallel()

	migrations, err := store.EmbeddedMigrations()
	require.NoError(t, err)
	require.NotEmpty(t, migrations, "the repository ships migrations")

	for i, m := range migrations {
		assert.NotEmpty(t, m.Name, "a migration must have a name")
		if i > 0 {
			assert.Greater(t, m.Version, migrations[i-1].Version,
				"migrations must be listed oldest first")
		}
	}

	latest, err := store.LatestMigrationVersion()
	require.NoError(t, err)
	assert.Equal(t, latest, migrations[len(migrations)-1].Version,
		"the latest version must be the last migration")
}

// TestMigrationStatusMatchesTheMigrator proves the read-only version check
// agrees with golang-migrate about where the database is.
func TestMigrationStatusMatchesTheMigrator(t *testing.T) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}
	ctx := context.Background()

	require.NoError(t, store.Migrate(url))

	current, latest, dirty, err := store.MigrationStatus(ctx, url)
	require.NoError(t, err)
	assert.False(t, dirty, "a freshly migrated database is not dirty")
	assert.Equal(t, latest, current, "the database is at the latest migration")

	version, dirty, err := store.MigrationVersion(url)
	require.NoError(t, err)
	assert.False(t, dirty)
	assert.Equal(t, version, current, "the read-only check must agree with the migrator")
}
