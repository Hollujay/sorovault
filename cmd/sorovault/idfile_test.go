package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadContractIDsFromFile(t *testing.T) {
	t.Parallel()

	path := t.TempDir() + "/ids.txt"
	require.NoError(t, os.WriteFile(path, []byte("\n# deployment\nC-file-1\n  C-file-2 # note\n"), 0o600))

	got, err := loadContractIDs([]string{"C-arg"}, path, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"C-arg", "C-file-1", "C-file-2"}, got)
}

func TestLoadContractIDsFromStdin(t *testing.T) {
	t.Parallel()

	got, err := loadContractIDs(nil, "-", strings.NewReader("C-one\nC-two\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"C-one", "C-two"}, got)
}

func TestLoadContractIDsRejectsUnreadableFile(t *testing.T) {
	t.Parallel()

	_, err := loadContractIDs(nil, t.TempDir()+"/missing.txt", nil)
	assert.ErrorContains(t, err, "opening contract ID file")
}

func TestAddAndRefreshAcceptFromFile(t *testing.T) {
	t.Parallel()

	add := newAddCmd()
	assert.NotNil(t, add.Flags().Lookup("from-file"))
	assert.NoError(t, add.Flags().Set("from-file", "ids.txt"))
	assert.NoError(t, add.Args(add, nil))

	refresh := newRefreshCmd()
	assert.NotNil(t, refresh.Flags().Lookup("from-file"))
	assert.NoError(t, refresh.Flags().Set("from-file", "ids.txt"))
	assert.NoError(t, refresh.Args(refresh, nil))
	assert.NoError(t, refresh.Flags().Set("all", "true"))
	assert.Error(t, refresh.Args(refresh, nil), "--all cannot combine with --from-file")
}
