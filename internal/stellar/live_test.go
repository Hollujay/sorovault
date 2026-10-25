package stellar

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLiveSmoke is an opt-in smoke test against the real Stellar testnet. It
// is skipped unless SOROVAULT_LIVE=1, so neither CI nor a plain `go test`
// ever touches the network. It fetches the zkvote contract through the real
// RPCFetcher and checks that a Stellar Asset Contract is reported as having no
// uploaded WASM.
func TestLiveSmoke(t *testing.T) {
	if os.Getenv("SOROVAULT_LIVE") != "1" {
		t.Skip("set SOROVAULT_LIVE=1 to run against testnet")
	}

	rpcURL := os.Getenv("TESTNET_RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://soroban-testnet.stellar.org"
	}

	fetcher := NewRPCFetcher(rpcURL, nil)
	t.Cleanup(func() { _ = fetcher.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	passphrase, err := fetcher.Network(ctx)
	require.NoError(t, err)
	assert.Equal(t, "testnet", NetworkName(passphrase))

	const zkvoteID = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	contract, err := fetcher.FetchContract(ctx, zkvoteID)
	require.NoError(t, err, "fetch zkvote from testnet")
	require.NotEmpty(t, contract.Wasm)

	// The module must be the one its code entry is keyed by.
	sum := sha256.Sum256(contract.Wasm)
	assert.Equal(t, contract.WasmHash, hex.EncodeToString(sum[:]))

	hash, err := fetcher.WasmHash(ctx, zkvoteID)
	require.NoError(t, err)
	assert.Equal(t, contract.WasmHash, hash)

	// The native XLM asset contract is built into the host: it has no WASM
	// and is served from the built-in interface instead.
	const nativeSAC = "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC"
	sac, err := fetcher.FetchContract(ctx, nativeSAC)
	require.NoError(t, err)
	assert.True(t, sac.Builtin)
	assert.Equal(t, BuiltinSACIdentifier, sac.WasmHash)
}
