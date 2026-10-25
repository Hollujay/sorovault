package store_test

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/store"
)

// poolURL names a database that need not exist: parsing a connection string
// does not touch the network, which is what lets the pool rules be checked
// without a database.
const poolURL = "postgres://sorovault:sorovault@localhost:5432/sorovault?sslmode=disable" //nolint:gosec // a throwaway test URL that is only parsed, never dialled

func TestResolvePoolConfig(t *testing.T) {
	t.Parallel()

	t.Run("pgx's own default applies when nothing is configured", func(t *testing.T) {
		cfg, err := store.ResolvePoolConfig(poolURL, store.PoolOptions{})
		require.NoError(t, err)

		// pgx defaults the maximum to the greater of 4 or the number of
		// CPUs, and leaves the minimum at none.
		want := int32(4)
		if cpu := int32(runtime.NumCPU()); cpu > want { //nolint:gosec // a CPU count fits in int32
			want = cpu
		}
		assert.Equal(t, want, cfg.MaxConns)
		assert.Zero(t, cfg.MinConns)
	})

	t.Run("the connection string sets the size when nothing overrides it", func(t *testing.T) {
		cfg, err := store.ResolvePoolConfig(poolURL+"&pool_max_conns=7&pool_min_conns=3", store.PoolOptions{})
		require.NoError(t, err)
		assert.Equal(t, int32(7), cfg.MaxConns)
		assert.Equal(t, int32(3), cfg.MinConns)
	})

	t.Run("an explicit size wins over the connection string", func(t *testing.T) {
		cfg, err := store.ResolvePoolConfig(poolURL+"&pool_max_conns=7&pool_min_conns=3",
			store.PoolOptions{MaxConns: 20, MinConns: 5})
		require.NoError(t, err)
		assert.Equal(t, int32(20), cfg.MaxConns)
		assert.Equal(t, int32(5), cfg.MinConns)
	})

	t.Run("an unset size leaves the connection string alone", func(t *testing.T) {
		cfg, err := store.ResolvePoolConfig(poolURL+"&pool_max_conns=7", store.PoolOptions{MinConns: 3})
		require.NoError(t, err)
		assert.Equal(t, int32(7), cfg.MaxConns)
		assert.Equal(t, int32(3), cfg.MinConns)
	})

	t.Run("a minimum above the maximum is refused", func(t *testing.T) {
		_, err := store.ResolvePoolConfig(poolURL, store.PoolOptions{MaxConns: 2, MinConns: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds its maximum")
	})

	t.Run("a minimum above the connection string's maximum is refused", func(t *testing.T) {
		_, err := store.ResolvePoolConfig(poolURL+"&pool_max_conns=2", store.PoolOptions{MinConns: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds its maximum")
	})

	t.Run("an unparseable connection string is an error", func(t *testing.T) {
		_, err := store.ResolvePoolConfig("://not a connection string", store.PoolOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "store: parsing postgres connection string")
	})
}

// TestNewPostgresAppliesPoolOptions checks the wiring end to end against a
// real database, which is the only place the pool is actually built.
func TestNewPostgresAppliesPoolOptions(t *testing.T) {
	url := os.Getenv(TestDatabaseURLEnv)
	if url == "" {
		t.Skip(TestDatabaseURLEnv + " not set")
	}

	s, err := store.NewPostgres(context.Background(), url, store.PoolOptions{MaxConns: 3, MinConns: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	pool := s.PoolConfig()
	assert.Equal(t, int32(3), pool.MaxConns)
	assert.Equal(t, int32(1), pool.MinConns)
}
