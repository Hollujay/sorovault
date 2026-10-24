package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// A contract ID shaped like a real one, for the flag-registration tests.
const sampleContractID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"

// TestContractCommandsRegisterCompletion checks that the four commands the
// issue names actually wired a completion function up, since a missing call is
// silent until a user presses Tab.
func TestContractCommandsRegisterCompletion(t *testing.T) {
	t.Parallel()

	commands := map[string]func() *cobra.Command{
		"get":      newGetCmd,
		"versions": newVersionsCmd,
		"refresh":  newRefreshCmd,
		"codegen":  newCodegenCmd,
	}

	for name, build := range commands {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := build()
			assert.NotNil(t, cmd.ValidArgsFunction, "%s should complete its contract ID argument", name)
		})
	}
}

// TestContractCompletionRendersNames covers the candidate format: a named
// contract is offered as "ID\tNAME" so the list is readable, and an unnamed one
// as the bare ID.
func TestContractCompletionRendersNames(t *testing.T) {
	t.Parallel()

	got := renderContractCandidates([]contractCandidate{
		{ContractID: "CAAA", Name: "zkvote"},
		{ContractID: "CBBB"},
	}, "")

	require.Len(t, got, 2)
	assert.Equal(t, "CAAA\tzkvote", got[0], "a named contract carries its name as a description")
	assert.Equal(t, "CBBB", got[1], "an unnamed contract is the bare ID")
}

// TestContractCompletionFiltersOnPrefix covers the toComplete filter, including
// the case-insensitivity a user typing a lowercase ID expects.
func TestContractCompletionFiltersOnPrefix(t *testing.T) {
	t.Parallel()

	all := []contractCandidate{
		{ContractID: "CDZZ123"},
		{ContractID: "CABC456"},
	}

	assert.Len(t, renderContractCandidates(all, ""), 2, "an empty prefix matches everything")
	assert.Len(t, renderContractCandidates(all, "CD"), 1)
	assert.Len(t, renderContractCandidates(all, "cd"), 1, "contract IDs are typed in either case")
	assert.Empty(t, renderContractCandidates(all, "ZZZ"))
	assert.Len(t, renderContractCandidates(all, "CDZZ123"), 1, "an exact ID is its own completion")
	assert.Empty(t, renderContractCandidates(all, "CDZZ1234"), "a longer prefix matches nothing")
}

// TestContractCompletionIsCapped keeps a huge registry from flooding the shell.
func TestContractCompletionIsCapped(t *testing.T) {
	t.Parallel()

	many := make([]contractCandidate, maxCompletionItems*2)
	for i := range many {
		many[i] = contractCandidate{ContractID: string(rune('A' + i%26))}
	}

	assert.LessOrEqual(t, len(renderContractCandidates(many, "")), maxCompletionItems)
}

// TestListContractCandidatesReadsTheRegistry covers the successful lookup the
// completion is built on. It runs against the in-memory store, which is the
// only way to exercise the query without a Postgres instance.
func TestListContractCandidatesReadsTheRegistry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := store.NewMemory()

	require.NoError(t, db.Save(ctx, store.Contract{
		ContractID:      "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		Network:         "testnet",
		CurrentWasmHash: "hash-a",
		Name:            "zkvote",
	}, &model.Interface{}))
	require.NoError(t, db.Save(ctx, store.Contract{
		ContractID:      "CBQZ0000000000000000000000000000000000000000000000000000",
		Network:         "testnet",
		CurrentWasmHash: "hash-b",
	}, &model.Interface{}))

	got, err := listContractCandidates(ctx, db)
	require.NoError(t, err)
	require.Len(t, got, 2, "both stored contracts are candidates")

	byID := map[string]string{}
	for _, c := range got {
		byID[c.ContractID] = c.Name
	}
	assert.Equal(t, "zkvote", byID["CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"])
	assert.Equal(t, "", byID["CBQZ0000000000000000000000000000000000000000000000000000"])
}

// TestContractCompletionEndToEnd joins the registry lookup to the rendering, so
// the two halves are exercised together rather than only in isolation.
func TestContractCompletionEndToEnd(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := store.NewMemory()

	require.NoError(t, db.Save(ctx, store.Contract{
		ContractID:      "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		Network:         "testnet",
		CurrentWasmHash: "hash-a",
		Name:            "zkvote",
	}, &model.Interface{}))

	candidates, err := listContractCandidates(ctx, db)
	require.NoError(t, err)

	all := renderContractCandidates(candidates, "")
	require.Len(t, all, 1)
	assert.Equal(t,
		"CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN\tzkvote",
		all[0], "a named contract is offered with its name as a description")

	assert.Empty(t, renderContractCandidates(candidates, "CB"), "a prefix that matches nothing offers nothing")
}

// TestContractCompletionSurvivesAFailedLookup is the requirement most likely to
// be broken by a naive implementation: completion runs on every Tab, so a
// database that is down must yield no candidates and no error, never a message
// pasted into the user's command line or a broken shell prompt.
func TestContractCompletionSurvivesAFailedLookup(t *testing.T) {
	t.Parallel()

	got := completeContractIDsFrom(func(context.Context) ([]contractCandidate, error) {
		return nil, errors.New("connection refused")
	}, "")

	assert.Empty(t, got, "an unreachable database offers no candidates rather than failing")
}

// TestContractCompletionReturnsCandidates covers the success path through the
// same seam, so a failure is not the only thing the injection can express.
func TestContractCompletionReturnsCandidates(t *testing.T) {
	t.Parallel()

	got := completeContractIDsFrom(func(context.Context) ([]contractCandidate, error) {
		return []contractCandidate{
			{ContractID: "CDZZ123", Name: "zkvote"},
			{ContractID: "CABC456"},
		}, nil
	}, "cdzz")

	require.Len(t, got, 1)
	assert.Equal(t, "CDZZ123\tzkvote", got[0])
}

// TestContractCompletionStopsAfterTheArgument covers the second-position case:
// these commands take one contract ID, so offering another would be wrong.
func TestContractCompletionStopsAfterTheArgument(t *testing.T) {
	t.Parallel()

	cmd := newGetCmd()
	got, directive := cmd.ValidArgsFunction(cmd, []string{sampleContractID}, "")

	assert.Empty(t, got)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// TestRefreshCompletionIsSilentWithAll covers the one command whose IDs are
// optional: with --all set, suggesting contract IDs would offer arguments the
// command rejects.
func TestRefreshCompletionIsSilentWithAll(t *testing.T) {
	t.Parallel()

	cmd := newRefreshCmd()
	require.NoError(t, cmd.Flags().Set("all", "true"))

	got, directive := cmd.ValidArgsFunction(cmd, nil, "")

	assert.Empty(t, got, "--all takes no contract IDs, so none are suggested")
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}

// TestNetworkCompletionOffersWellKnownNames covers --network completion, which
// must work with no database at all because the three public networks are
// always valid.
func TestNetworkCompletionOffersWellKnownNames(t *testing.T) {
	t.Parallel()

	got := completeNetworks()

	assert.Contains(t, got, stellar.NetworkName(stellar.NetworkPublic))
	assert.Contains(t, got, stellar.NetworkName(stellar.NetworkTestnet))
	assert.Contains(t, got, stellar.NetworkName(stellar.NetworkFuturenet))
	assert.NotContains(t, got, "", "an empty name is never a candidate")
}

// TestNetworkCompletionDeduplicates keeps the well-known names from being
// offered twice when the registry already records them.
func TestNetworkCompletionDeduplicates(t *testing.T) {
	t.Parallel()

	seen := map[string]int{}
	for _, name := range completeNetworks() {
		seen[name]++
	}
	for name, count := range seen {
		assert.Equal(t, 1, count, "network %q should be offered once", name)
	}
}

// TestNetworkFlagRegistration checks that the commands with a --network flag
// registered a completion for it.
func TestNetworkFlagRegistration(t *testing.T) {
	t.Parallel()

	for name, build := range map[string]func() *cobra.Command{
		"get":      newGetCmd,
		"versions": newVersionsCmd,
		"codegen":  newCodegenCmd,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cmd := build()
			fn, ok := cmd.GetFlagCompletionFunc("network")
			require.True(t, ok, "%s should complete --network", name)
			assert.NotNil(t, fn)
		})
	}
}

// TestPrefixHelpers pin the small pure helpers the completion relies on.
func TestPrefixHelpers(t *testing.T) {
	t.Parallel()

	assert.True(t, hasPrefixFold("CDZZ", ""))
	assert.True(t, hasPrefixFold("CDZZ", "cd"))
	assert.True(t, hasPrefixFold("CDZZ", "CDZZ"))
	assert.False(t, hasPrefixFold("CDZZ", "CDZZZ"))
	assert.False(t, hasPrefixFold("CD", "CDZZ"))

	assert.True(t, equalFoldASCII("abc", "ABC"))
	assert.False(t, equalFoldASCII("abc", "abd"))
	assert.False(t, equalFoldASCII("abc", "ab"))
}

// TestDebugfStaysOffStdout guards the rule that completion must never write to
// stdout, whatever the log level.
func TestDebugfStaysOffStdout(t *testing.T) {
	// No t.Parallel: this touches the environment.
	t.Setenv("LOG_LEVEL", "debug")

	// Capture the real stdout around the call; anything written there would
	// be pasted into the user's command line.
	orig := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	debugf("test %d", 1)
	os.Stdout = orig
	require.NoError(t, w.Close())

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	require.NoError(t, r.Close())

	assert.Empty(t, strings.TrimSpace(string(buf[:n])), "diagnostics go to stderr, never stdout")
}
