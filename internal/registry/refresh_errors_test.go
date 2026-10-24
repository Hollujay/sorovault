package registry_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// errSentinel is an error no part of SoroVault produces, so a test can prove an
// error passed straight through rather than being wrapped or swallowed.
var errSentinel = errors.New("refresh exploded")

// currentInterface returns the name of the first function in the contract's
// current stored interface, or "" when there is none.
func currentInterface(t *testing.T, db store.Store, network, contractID string) string {
	t.Helper()

	got, err := db.GetSpec(context.Background(), network, contractID, "")
	if err != nil {
		return ""
	}
	if len(got.Interface.Functions) == 0 {
		return ""
	}
	return got.Interface.Functions[0].Name
}

func TestRefreshUnknownContractReturnsNotFound(t *testing.T) {
	t.Parallel()

	reg, _, db := newRegistry(t)

	// The contract was never registered, so there is nothing to refresh.
	_, err := reg.Refresh(context.Background(), idA)
	require.ErrorIs(t, err, store.ErrNotFound)

	// Nothing was stored for it.
	_, err = db.GetContract(context.Background(), "testnet", idA)
	assert.ErrorIs(t, err, store.ErrNotFound, "refreshing an unknown contract must not create a record")
}

func TestRefreshWasmHashFailureLeavesContractUnchanged(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	fake.Deploy(idA, moduleWith(t, "v1"))
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err)

	// The hash lookup itself fails, before any module is downloaded.
	fake.HashErr = errSentinel

	_, err = reg.Refresh(context.Background(), idA)
	require.ErrorIs(t, err, errSentinel)

	// The stored contract and interface are untouched.
	got, err := db.GetContract(context.Background(), "testnet", idA)
	require.NoError(t, err)
	assert.Nil(t, got.MissingSince, "an RPC error must not mark the contract missing")
	assert.Equal(t, "v1", currentInterface(t, db, "testnet", idA))
}

func TestRefreshFetchFailureAfterHashChangeLeavesContractUnchanged(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	fake.Deploy(idA, moduleWith(t, "v1"))
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err)

	// The contract upgrades, so the hash changes and Refresh will try to
	// download the new module.
	fake.Upgrade(idA, moduleWith(t, "v2"))

	// The download fails after the hash changed.
	fake.FetchErr = errSentinel

	_, err = reg.Refresh(context.Background(), idA)
	require.ErrorIs(t, err, errSentinel)

	// The failed half-upgrade must not clobber the old interface.
	got, err := db.GetContract(context.Background(), "testnet", idA)
	require.NoError(t, err)
	assert.Nil(t, got.MissingSince)
	assert.Equal(t, "v1", currentInterface(t, db, "testnet", idA),
		"a module that cannot be downloaded must not replace the stored interface")

	versions, err := db.ListVersions(context.Background(), "testnet", idA)
	require.NoError(t, err)
	assert.Len(t, versions, 1, "no new version was stored")
}

func TestRefreshDecodeFailureKeepsOldInterface(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	fake.Deploy(idA, moduleWith(t, "v1"))
	_, err := reg.Register(context.Background(), idA)
	require.NoError(t, err)

	// The contract upgrades to a module SoroVault cannot decode: a corrupt
	// spec section built by hand with wasmtest.Module.
	fake.Upgrade(idA, wasmtest.Module(wasmtest.Section{Name: "producers", Body: []byte("x")}))

	_, err = reg.Refresh(context.Background(), idA)
	require.ErrorIs(t, err, spec.ErrNoSpecSection)

	// The last interface that did decode must still be served: the failed
	// decode must not leave the contract pointing at an interface that was
	// never stored.
	assert.Equal(t, "v1", currentInterface(t, db, "testnet", idA))

	versions, err := db.ListVersions(context.Background(), "testnet", idA)
	require.NoError(t, err)
	assert.Len(t, versions, 1, "the undecodable module must not add a version")
}
