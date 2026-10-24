package registry_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/registry"
	"github.com/soroworks/sorovault/internal/spec"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/stellar/stellartest"
	"github.com/soroworks/sorovault/internal/store"
	"github.com/soroworks/sorovault/internal/wasm"
	"github.com/soroworks/sorovault/internal/wasm/wasmtest"
)

// hashMismatchFetcher serves different bytes than the hash the caller asked
// for, which is how a corrupt or hostile RPC response reaches CatalogWasm.
type hashMismatchFetcher struct {
	stellar.Fetcher
	wasm []byte
}

func (f hashMismatchFetcher) FetchWasm(context.Context, string) ([]byte, error) {
	return f.wasm, nil
}

// TestCatalogWasmRejectsMalformedHash proves a hash that cannot name a WASM
// module is refused up front, before the registry talks to the network.
func TestCatalogWasmRejectsMalformedHash(t *testing.T) {
	t.Parallel()

	valid := strings.Repeat("ab", sha256.Size)

	tests := []struct {
		name string
		hash string
	}{
		{"empty", ""},
		{"not hexadecimal", strings.Repeat("zz", sha256.Size)},
		{"too short", valid[:len(valid)-2]},
		{"too long", valid + "00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg, fake, _ := newRegistry(t)
			result, err := reg.CatalogWasm(context.Background(), tt.hash)
			require.Nil(t, result)
			require.ErrorIs(t, err, registry.ErrInvalidWasmHash)
			assert.Contains(t, err.Error(), "invalid wasm hash")
			assert.Zero(t, fake.Calls["Network"], "a malformed hash must be rejected before any RPC")
			assert.Zero(t, fake.Calls["FetchWasm"], "a malformed hash must not trigger a fetch")
		})
	}
}

// TestCatalogWasmMissingCode covers the 404 case: the hash is well-formed but
// the network has no uploaded code for it.
func TestCatalogWasmMissingCode(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	sum := sha256.Sum256([]byte("code that was never uploaded"))
	hash := hex.EncodeToString(sum[:])

	result, err := reg.CatalogWasm(context.Background(), hash)
	require.Nil(t, result)
	require.ErrorIs(t, err, stellar.ErrWasmNotFound,
		"a missing code entry must surface the not-found error the API maps to 404")
	assert.Contains(t, err.Error(), hash)
	assert.Equal(t, 1, fake.Calls["FetchWasm"])

	_, err = db.GetWasmSpec(context.Background(), "testnet", hash)
	assert.ErrorIs(t, err, store.ErrNotFound, "a failed fetch must not store an empty spec")
}

// TestCatalogWasmRejectsModuleWithoutSpec proves catalogueable code still has
// to describe a contract interface.
func TestCatalogWasmRejectsModuleWithoutSpec(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	// Fetchable and hash-correct, but with no "contractspecv0" section.
	module := wasmtest.Module(wasmtest.Section{Name: "producers", Body: []byte("rustc")})
	hash := fake.UploadWasm(module)

	result, err := reg.CatalogWasm(context.Background(), hash)
	require.Nil(t, result)
	require.ErrorIs(t, err, spec.ErrNoSpecSection)
	assert.Contains(t, err.Error(), hash, "the error should name the module that failed to decode")

	_, err = db.GetWasmSpec(context.Background(), "testnet", hash)
	assert.ErrorIs(t, err, store.ErrNotFound, "an undecodable module must not be catalogued")
}

// TestCatalogWasmRejectsOversizedModules covers both decode limits: a spec
// section larger than the decoder accepts, and a module larger than the WASM
// reader accepts.
func TestCatalogWasmRejectsOversizedModules(t *testing.T) {
	t.Parallel()

	oversizedModule := wasmtest.Module()
	oversizedModule = append(oversizedModule, make([]byte, wasm.MaxModuleSize)...)

	tests := []struct {
		name    string
		module  []byte
		wantErr []error
	}{
		{
			name: "spec section over the limit",
			module: wasmtest.Module(wasmtest.Section{
				Name: wasm.SectionContractSpec,
				Body: make([]byte, spec.MaxSpecSectionSize+1),
			}),
			wantErr: []error{spec.ErrLimitExceeded},
		},
		{
			name:    "module over the limit",
			module:  oversizedModule,
			wantErr: []error{spec.ErrLimitExceeded, wasm.ErrModuleTooLarge},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg, fake, db := newRegistry(t)
			hash := fake.UploadWasm(tt.module)

			result, err := reg.CatalogWasm(context.Background(), hash)
			require.Nil(t, result)
			for _, target := range tt.wantErr {
				require.ErrorIs(t, err, target)
			}
			assert.Contains(t, err.Error(), "limit")

			_, err = db.GetWasmSpec(context.Background(), "testnet", hash)
			assert.ErrorIs(t, err, store.ErrNotFound, "an oversized module must not be catalogued")
		})
	}
}

// TestCatalogWasmRejectsHashMismatch proves the bytes fetched for a hash are
// verified before they are decoded and stored.
func TestCatalogWasmRejectsHashMismatch(t *testing.T) {
	t.Parallel()

	requested := moduleWith(t, "claimed")
	hash := stellartest.HashOf(requested)
	served := wasmtest.Module(wasmtest.Section{Name: "producers", Body: []byte("other")})

	fake := stellartest.New()
	reg := registry.New(
		hashMismatchFetcher{Fetcher: fake, wasm: served},
		spec.XDRDecoder{},
		store.NewMemory(),
		registry.Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
	)

	result, err := reg.CatalogWasm(context.Background(), hash)
	require.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not match hash")
	assert.Contains(t, err.Error(), hash)
}

// TestCatalogWasmIsIdempotent proves cataloguing the same hash twice returns
// the stored result without a second download, and that an uppercase hash
// resolves to the same lowercase key.
func TestCatalogWasmIsIdempotent(t *testing.T) {
	t.Parallel()

	reg, fake, db := newRegistry(t)
	module := moduleWith(t, "proposed")
	hash := fake.UploadWasm(module)

	first, err := reg.CatalogWasm(context.Background(), hash)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.True(t, first.Created)
	assert.Equal(t, hash, first.WasmHash)
	assert.Equal(t, "testnet", first.Network)
	require.NotNil(t, first.Interface)
	require.Len(t, first.Interface.Functions, 1)
	assert.Equal(t, "proposed", first.Interface.Functions[0].Name)
	assert.False(t, first.DecodedAt.IsZero())

	fetches := fake.Calls["FetchWasm"]

	second, err := reg.CatalogWasm(context.Background(), strings.ToUpper(hash))
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.False(t, second.Created, "already-catalogued code is not created again")
	assert.Equal(t, first.WasmHash, second.WasmHash)
	assert.Equal(t, first.Network, second.Network)
	assert.Equal(t, first.DecodedAt, second.DecodedAt, "the stored row must be returned unchanged")
	assert.Equal(t, fetches, fake.Calls["FetchWasm"],
		"a cached catalogue entry must not be downloaded again")

	stored, err := db.GetWasmSpec(context.Background(), "testnet", hash)
	require.NoError(t, err)
	assert.Equal(t, first.DecodedAt, stored.DecodedAt)
	require.Len(t, stored.Interface.Functions, 1)
	assert.Equal(t, "proposed", stored.Interface.Functions[0].Name)
}
