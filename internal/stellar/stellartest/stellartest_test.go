package stellartest_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
)

const contractID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"

// tinyModule is the smallest module the fake needs: the WASM magic and
// version, with no sections. The fake hashes bytes and never parses them, so
// a header is enough to stand in for uploaded code.
func tinyModule() []byte {
	return []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
}

// upgradedModule is a different tiny module, so replacing a contract's code
// changes its hash the way a real upgrade does.
func upgradedModule() []byte {
	return append(tinyModule(), 0x00, 0x01, 0x00) // one empty custom section
}

func TestNewReportsTestnetByDefault(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()

	got, err := fake.Network(context.Background())
	require.NoError(t, err)
	assert.Equal(t, stellar.NetworkTestnet, got)
	assert.Equal(t, 1, fake.Calls["Network"])
}

// TestUploadWasmIsFetchableByHash covers code that exists on the network
// without a contract instance pointing at it.
func TestUploadWasmIsFetchableByHash(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	module := tinyModule()

	hash := fake.UploadWasm(module)
	assert.Equal(t, stellartest.HashOf(module), hash)
	assert.Len(t, hash, sha256.Size*2, "a WASM hash is 32 bytes of lowercase hex")

	got, err := fake.FetchWasm(context.Background(), hash)
	require.NoError(t, err)
	assert.Equal(t, module, got)
}

// TestDeployMakesContractFetchable proves a deployed contract resolves the
// same way the real client resolves one: instance, hash and module agree.
func TestDeployMakesContractFetchable(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	module := tinyModule()
	fake.Deploy(contractID, module)

	contract, err := fake.FetchContract(context.Background(), contractID)
	require.NoError(t, err)
	assert.Equal(t, contractID, contract.ID)
	assert.Equal(t, stellartest.HashOf(module), contract.WasmHash)
	assert.Equal(t, module, contract.Wasm)

	hash, err := fake.WasmHash(context.Background(), contractID)
	require.NoError(t, err)
	assert.Equal(t, contract.WasmHash, hash)

	// FetchWasm also resolves code through a deployed instance, as the RPC
	// does when the code entry itself is what the caller asked for.
	got, err := fake.FetchWasm(context.Background(), contract.WasmHash)
	require.NoError(t, err)
	assert.Equal(t, module, got)
}

// TestUpgradeChangesWhatTheContractReturns proves an upgrade replaces the
// module and hash the contract resolves to.
func TestUpgradeChangesWhatTheContractReturns(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	fake.Deploy(contractID, tinyModule())

	before, err := fake.FetchContract(context.Background(), contractID)
	require.NoError(t, err)

	upgraded := upgradedModule()
	fake.Upgrade(contractID, upgraded)

	after, err := fake.FetchContract(context.Background(), contractID)
	require.NoError(t, err)
	assert.NotEqual(t, before.WasmHash, after.WasmHash, "different code must have a different hash")
	assert.Equal(t, stellartest.HashOf(upgraded), after.WasmHash)
	assert.Equal(t, upgraded, after.Wasm)

	hash, err := fake.WasmHash(context.Background(), contractID)
	require.NoError(t, err)
	assert.Equal(t, after.WasmHash, hash)

	got, err := fake.FetchWasm(context.Background(), after.WasmHash)
	require.NoError(t, err)
	assert.Equal(t, upgraded, got)
}

// TestRemoveMakesContractNotFound covers the not-found path every registry
// test relies on: both contract lookups report the sentinel the real client
// returns for an absent or expired entry.
func TestRemoveMakesContractNotFound(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	fake.Deploy(contractID, tinyModule())
	fake.Remove(contractID)

	_, err := fake.FetchContract(context.Background(), contractID)
	require.ErrorIs(t, err, stellar.ErrContractNotFound)
	assert.Contains(t, err.Error(), contractID)

	_, err = fake.WasmHash(context.Background(), contractID)
	require.ErrorIs(t, err, stellar.ErrContractNotFound)
	assert.Contains(t, err.Error(), contractID)
}

// TestHashHelpersAgreeWithSHA256 pins the fake's hashing to the network's rule
// so a test cannot pass against a made-up key.
func TestHashHelpersAgreeWithSHA256(t *testing.T) {
	t.Parallel()

	module := tinyModule()
	sum := sha256.Sum256(module)
	want := hex.EncodeToString(sum[:])

	assert.Equal(t, want, stellartest.HashOf(module))

	fake := stellartest.New()
	assert.Equal(t, want, fake.UploadWasm(module))

	fake.Deploy(contractID, module)
	got, err := fake.WasmHash(context.Background(), contractID)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

// TestFetchedBytesAreIndependent proves a caller mutating bytes it supplied or
// received cannot change what the fake holds, which is how the network behaves
// and what registry tests depend on.
func TestFetchedBytesAreIndependent(t *testing.T) {
	t.Parallel()

	fake := stellartest.New()
	original := tinyModule()
	hash := fake.UploadWasm(original)

	original[0] = 0xff
	first, err := fake.FetchWasm(context.Background(), hash)
	require.NoError(t, err)
	assert.Equal(t, tinyModule(), first)

	first[0] = 0xff
	second, err := fake.FetchWasm(context.Background(), hash)
	require.NoError(t, err)
	assert.Equal(t, tinyModule(), second)
}

// TestConfiguredErrorsAreReturned covers the injection fields registry and
// refresher tests use to simulate RPC failures.
func TestConfiguredErrorsAreReturned(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("rpc unavailable")
	fake := stellartest.New()
	fake.Deploy(contractID, tinyModule())

	fake.FetchErr = sentinel
	_, err := fake.FetchContract(context.Background(), contractID)
	require.ErrorIs(t, err, sentinel)

	fake.FetchErr = nil
	fake.HashErr = sentinel
	_, err = fake.WasmHash(context.Background(), contractID)
	require.ErrorIs(t, err, sentinel)

	fake.HashErr = nil
	fake.NetworkErr = sentinel
	_, err = fake.Network(context.Background())
	require.ErrorIs(t, err, sentinel)
}

// TestBeforeFetchCanRefuse proves the hook runs before both contract lookups
// and can fail them, which is how the registry's concurrency tests hold a
// fetch open.
func TestBeforeFetchCanRefuse(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("held open")
	fake := stellartest.New()
	fake.Deploy(contractID, tinyModule())

	var seen []string
	fake.BeforeFetch = func(_ context.Context, id string) error {
		seen = append(seen, id)
		return sentinel
	}

	_, err := fake.FetchContract(context.Background(), contractID)
	require.ErrorIs(t, err, sentinel)

	_, err = fake.WasmHash(context.Background(), contractID)
	require.ErrorIs(t, err, sentinel)

	assert.Equal(t, []string{contractID, contractID}, seen)
	assert.Zero(t, fake.Calls["FetchContract"], "a refused fetch never reaches the lookup")
	assert.Zero(t, fake.Calls["WasmHash"])
}
