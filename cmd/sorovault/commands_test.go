package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/store"
)

func TestPrintVersions(t *testing.T) {
	t.Parallel()

	versions := []store.SpecVersion{
		{WasmHash: "newer-hash", DecodedAt: time.Date(2026, 10, 2, 9, 15, 0, 0, time.UTC), Current: true},
		{WasmHash: "older-hash", DecodedAt: time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)},
	}

	var out bytes.Buffer
	printVersions(&out, versions)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	require.Len(t, lines, 3, "a header and one row per version")
	assert.Contains(t, lines[0], "WASM HASH")
	assert.Contains(t, lines[0], "DECODED")
	assert.Contains(t, lines[0], "CURRENT")

	assert.Contains(t, lines[1], "newer-hash")
	assert.Contains(t, lines[1], "2026-10-02 09:15")
	assert.Contains(t, lines[1], "yes")

	assert.Contains(t, lines[2], "older-hash")
	assert.Contains(t, lines[2], "2026-09-01 08:00")
	assert.NotContains(t, lines[2], "yes", "a superseded version is not marked current")
}

// TestVersionsJSONShape keeps the command's JSON the same shape the API
// serves, so a pipeline can read either.
func TestVersionsJSONShape(t *testing.T) {
	t.Parallel()

	doc := versionsDocument{
		ContractID: "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		Network:    "testnet",
		Versions:   []store.SpecVersion{{WasmHash: "abc", Current: true}},
	}

	raw, err := json.Marshal(doc)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, doc.ContractID, decoded["contract_id"])
	assert.Equal(t, "testnet", decoded["network"])

	versions, ok := decoded["versions"].([]any)
	require.True(t, ok)
	require.Len(t, versions, 1)
	assert.Equal(t, "abc", versions[0].(map[string]any)["wasm_hash"])
	assert.Equal(t, true, versions[0].(map[string]any)["current"])
}

func TestRefreshAllCommandArgs(t *testing.T) {
	t.Parallel()

	cmd := newRefreshCmd()
	require.Error(t, cmd.Args(cmd, nil), "without --all a contract ID is required")
	require.NoError(t, cmd.Args(cmd, []string{"a"}))

	require.NoError(t, cmd.Flags().Set("all", "true"))
	require.NoError(t, cmd.Args(cmd, nil), "--all needs no contract IDs")
	require.Error(t, cmd.Args(cmd, []string{"a"}), "--all and contract IDs are mutually exclusive")
	require.NotNil(t, cmd.Flags().Lookup("json"))
}

func TestConfirmed(t *testing.T) {
	t.Parallel()

	for _, answer := range []string{"y\n", "yes\n", " YES \n"} {
		assert.True(t, confirmed(strings.NewReader(answer)), "answer %q", answer)
	}
	for _, answer := range []string{"n\n", "no\n", "\n", ""} {
		assert.False(t, confirmed(strings.NewReader(answer)), "answer %q", answer)
	}
}

func TestRemoveCommandFlags(t *testing.T) {
	t.Parallel()

	cmd := newRemoveCmd()
	assert.NotNil(t, cmd.Flags().Lookup("network"))
	assert.NotNil(t, cmd.Flags().Lookup("yes"))
	require.Error(t, cmd.Args(cmd, nil))
	require.Error(t, cmd.Args(cmd, []string{"a", "b"}))
	require.NoError(t, cmd.Args(cmd, []string{"a"}))
}

func TestExportImportCommandFlags(t *testing.T) {
	t.Parallel()

	export := newExportCmd()
	assert.NotNil(t, export.Flags().Lookup("output"))
	require.NoError(t, export.Args(export, nil))
	require.Error(t, export.Args(export, []string{"a", "b"}))

	importCmd := newImportCmd()
	assert.NotNil(t, importCmd.Flags().Lookup("input"))
	assert.NotNil(t, importCmd.Flags().Lookup("merge"))
	require.NoError(t, importCmd.Args(importCmd, nil))
	require.Error(t, importCmd.Args(importCmd, []string{"a", "b"}))
}

func TestPackageDirName(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "zkvote", packageDirName("@acme/zkvote"), "a scope is not a directory")
	assert.Equal(t, "zkvote-client", packageDirName("zkvote-client"))
}

func TestVersionsCommandTakesOneContract(t *testing.T) {
	t.Parallel()

	cmd := newVersionsCmd()
	assert.NotNil(t, cmd.Flags().Lookup("network"))
	assert.NotNil(t, cmd.Flags().Lookup("json"))

	require.Error(t, cmd.Args(cmd, nil), "a contract ID is required")
	require.Error(t, cmd.Args(cmd, []string{"a", "b"}), "only one contract ID is accepted")
	require.NoError(t, cmd.Args(cmd, []string{"a"}))
}

func TestPrintOrigin(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	printOrigin(&out, store.Contract{})
	assert.Equal(t, "  creator    unknown\n", out.String())

	out.Reset()
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	printOrigin(&out, store.Contract{Creator: "GCREATOR", CreatedLedger: 42, VerifiedBy: "GCREATOR", VerifiedAt: &at})
	assert.Equal(t, "  creator    GCREATOR (ledger 42)\n  verified   by GCREATOR on 2026-08-07\n", out.String())
}

func TestClaimSignCommand(t *testing.T) {
	kp := keypair.MustRandom()
	t.Setenv(secretEnv, kp.Seed())

	cmd := newClaimCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"sign", "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
		"--network", "testnet", "--name", "Hello", "--source-url", "https://github.com/example/hello",
		"--issued-at", "2026-08-07T12:00:00Z",
	})
	require.NoError(t, cmd.Execute())

	var req struct {
		Claim     attest.Claim `json:"claim"`
		Signer    string       `json:"signer"`
		Signature string       `json:"signature"`
	}
	require.NoError(t, json.Unmarshal(out.Bytes(), &req))
	assert.Equal(t, kp.Address(), req.Signer)
	assert.Equal(t, "Hello", req.Claim.Name)
	assert.NoError(t, attest.VerifyClaim(req.Signer, req.Signature, req.Claim), "the output is what the API verifies")
	assert.NotContains(t, out.String(), kp.Seed(), "the secret must never be printed")

	t.Run("refuses without a secret", func(t *testing.T) {
		t.Setenv(secretEnv, "")
		cmd := newClaimCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"sign", "C", "--network", "testnet", "--name", "n", "--source-url", "https://x.example"})
		assert.ErrorContains(t, cmd.Execute(), secretEnv)
	})
}

func TestKeygenCommand(t *testing.T) {
	t.Parallel()

	cmd := newKeygenCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	require.NoError(t, cmd.Execute())

	seed, err := attest.ParseSeed(strings.TrimSpace(stdout.String()))
	require.NoError(t, err)
	signer, err := attest.NewSigner(seed)
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), signer.KeyID())
	assert.NotContains(t, stderr.String(), strings.TrimSpace(stdout.String()), "the seed goes to stdout only")
}
