package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/config"
)

// readLog builds a logger at level, writes one line at each level through
// it, and returns what reached the file.
func readLog(t *testing.T, level slog.Level) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sorovault.log")
	f, err := os.Create(path) //nolint:gosec // path is inside t.TempDir()
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	cfg := &config.Config{LogLevel: level}
	logger := cfg.NewLogger(f)
	require.NotNil(t, logger)

	logger.Debug("debug line")
	logger.Info("info line", "contract", "CABC")
	logger.Warn("warn line")
	logger.Error("error line")
	require.NoError(t, f.Sync())

	data, err := os.ReadFile(path) //nolint:gosec // path is inside t.TempDir()
	require.NoError(t, err)
	return string(data)
}

func TestNewLoggerWritesTextAtTheConfiguredLevel(t *testing.T) {
	t.Parallel()

	out := readLog(t, slog.LevelInfo)

	assert.NotContains(t, out, "debug line")
	assert.Contains(t, out, "level=INFO msg=\"info line\" contract=CABC")
	assert.Contains(t, out, "level=WARN")
	assert.Contains(t, out, "level=ERROR")
}

func TestNewLoggerAtDebugKeepsEverything(t *testing.T) {
	t.Parallel()

	assert.Contains(t, readLog(t, slog.LevelDebug), "msg=\"debug line\"")
}

func TestNewLoggerAtErrorDropsLowerLevels(t *testing.T) {
	t.Parallel()

	out := readLog(t, slog.LevelError)

	assert.NotContains(t, out, "info line")
	assert.NotContains(t, out, "warn line")
	assert.Contains(t, out, "error line")
}
