package stellar_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stellar/go-stellar-sdk/keypair"
	"github.com/stellar/go-stellar-sdk/xdr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/stellar"
)

// chainRPC is a JSON-RPC server that answers the history methods origin and
// event lookups use, from canned responses.
type chainRPC struct {
	t *testing.T
	// instanceLedger is the lastModifiedLedgerSeq of the contract's instance
	// entry; zero means the entry does not exist.
	instanceLedger uint32
	transactions   []map[string]any
	// failures maps a method to the error message it returns.
	failures map[string]string
	events   []map[string]any
	// requests records each call's method and raw params.
	requests []rpcRequest
}

func (c *chainRPC) start() *stellar.RPCFetcher {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		require.NoError(c.t, json.NewDecoder(r.Body).Decode(&req))
		c.requests = append(c.requests, req)

		reply := func(result any) {
			require.NoError(c.t, json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}))
		}
		if msg, ok := c.failures[req.Method]; ok {
			require.NoError(c.t, json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32600, "message": msg},
			}))
			return
		}

		switch req.Method {
		case "getLedgerEntries":
			entries := []map[string]any{}
			if c.instanceLedger != 0 {
				entries = append(entries, map[string]any{
					"key": "k", "xdr": mustBase64(c.t, xdr.LedgerEntryData{
						Type: xdr.LedgerEntryTypeContractData,
						ContractData: &xdr.ContractDataEntry{
							Contract: contractAddress(c.t, testContractID),
							Key:      xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
							Val:      xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
						},
					}),
					"lastModifiedLedgerSeq": c.instanceLedger,
				})
			}
			reply(map[string]any{"entries": entries, "latestLedger": 500})
		case "getTransactions":
			reply(map[string]any{"transactions": c.transactions, "latestLedger": 500, "cursor": "next"})
		case "getEvents":
			reply(map[string]any{"events": c.events, "cursor": "0000000000000000500-0000000000", "latestLedger": 500})
		case "getLatestLedger":
			reply(map[string]any{"id": "x", "protocolVersion": 25, "sequence": 500})
		default:
			c.t.Errorf("unexpected method %s", req.Method)
		}
	}))
	c.t.Cleanup(srv.Close)

	f := stellar.NewRPCFetcher(srv.URL, nil)
	c.t.Cleanup(func() { _ = f.Close() })
	return f
}

func mustBase64(t *testing.T, v any) string {
	t.Helper()
	s, err := xdr.MarshalBase64(v)
	require.NoError(t, err)
	return s
}

// creationTx builds a successful transaction in ledger that creates the
// instance of contractID, submitted by source (and, if opSource is not
// empty, with that account as the operation's own source).
func creationTx(t *testing.T, ledger uint32, contractID, source, opSource string) map[string]any {
	t.Helper()

	op := xdr.Operation{Body: xdr.OperationBody{
		Type:           xdr.OperationTypeBumpSequence,
		BumpSequenceOp: &xdr.BumpSequenceOp{BumpTo: 1},
	}}
	if opSource != "" {
		muxed := xdr.MustMuxedAddress(opSource)
		op.SourceAccount = &muxed
	}
	env := xdr.TransactionEnvelope{
		Type: xdr.EnvelopeTypeEnvelopeTypeTx,
		V1: &xdr.TransactionV1Envelope{Tx: xdr.Transaction{
			SourceAccount: xdr.MustMuxedAddress(source),
			Operations:    []xdr.Operation{op},
		}},
	}

	created := xdr.LedgerEntry{Data: xdr.LedgerEntryData{
		Type: xdr.LedgerEntryTypeContractData,
		ContractData: &xdr.ContractDataEntry{
			Contract: contractAddress(t, contractID),
			Key:      xdr.ScVal{Type: xdr.ScValTypeScvLedgerKeyContractInstance},
			Val:      xdr.ScVal{Type: xdr.ScValTypeScvVoid},
		},
	}}
	meta := xdr.TransactionMeta{V: 3, V3: &xdr.TransactionMetaV3{
		Operations: []xdr.OperationMeta{{Changes: xdr.LedgerEntryChanges{{
			Type: xdr.LedgerEntryChangeTypeLedgerEntryCreated, Created: &created,
		}}}},
	}}

	return map[string]any{
		"status": "SUCCESS", "ledger": ledger, "txHash": "ab",
		"envelopeXdr": mustBase64(t, env), "resultMetaXdr": mustBase64(t, meta),
	}
}

func TestFindOrigin(t *testing.T) {
	t.Parallel()

	deployer := keypair.MustRandom().Address()
	other := keypair.MustRandom().Address()
	ctx := context.Background()

	t.Run("credits the transaction's source account", func(t *testing.T) {
		t.Parallel()
		c := &chainRPC{t: t, instanceLedger: 100, transactions: []map[string]any{
			creationTx(t, 100, "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V", other, ""),
			creationTx(t, 100, testContractID, deployer, ""),
		}}
		origin, err := c.start().FindOrigin(ctx, testContractID)
		require.NoError(t, err)
		assert.Equal(t, &stellar.Origin{Creator: deployer, Ledger: 100}, origin)
	})

	t.Run("an operation's own source account wins", func(t *testing.T) {
		t.Parallel()
		c := &chainRPC{t: t, instanceLedger: 100, transactions: []map[string]any{
			creationTx(t, 100, testContractID, other, deployer),
		}}
		origin, err := c.start().FindOrigin(ctx, testContractID)
		require.NoError(t, err)
		assert.Equal(t, deployer, origin.Creator)
	})

	t.Run("a contract modified since creation is unknown, not guessed", func(t *testing.T) {
		t.Parallel()
		// The instance was last touched in ledger 100, but the transaction
		// there only updated it; nothing in the ledger created it.
		c := &chainRPC{t: t, instanceLedger: 100, transactions: []map[string]any{
			creationTx(t, 100, "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V", other, ""),
			{"status": "SUCCESS", "ledger": 101},
		}}
		_, err := c.start().FindOrigin(ctx, testContractID)
		assert.ErrorIs(t, err, stellar.ErrOriginUnknown)
	})

	t.Run("failed transactions are ignored", func(t *testing.T) {
		t.Parallel()
		tx := creationTx(t, 100, testContractID, deployer, "")
		tx["status"] = "FAILED"
		c := &chainRPC{t: t, instanceLedger: 100, transactions: []map[string]any{tx}}
		_, err := c.start().FindOrigin(ctx, testContractID)
		assert.ErrorIs(t, err, stellar.ErrOriginUnknown)
	})

	t.Run("a ledger outside the RPC's retention is unknown", func(t *testing.T) {
		t.Parallel()
		c := &chainRPC{t: t, instanceLedger: 3, failures: map[string]string{
			"getTransactions": "startLedger must be within the ledger range: 400 - 500",
		}}
		_, err := c.start().FindOrigin(ctx, testContractID)
		assert.ErrorIs(t, err, stellar.ErrOriginUnknown)
	})

	t.Run("other RPC failures are errors, not unknown", func(t *testing.T) {
		t.Parallel()
		c := &chainRPC{t: t, instanceLedger: 100, failures: map[string]string{"getTransactions": "database is on fire"}}
		_, err := c.start().FindOrigin(ctx, testContractID)
		require.Error(t, err)
		assert.NotErrorIs(t, err, stellar.ErrOriginUnknown)
	})

	t.Run("an absent contract is not found", func(t *testing.T) {
		t.Parallel()
		c := &chainRPC{t: t}
		_, err := c.start().FindOrigin(ctx, testContractID)
		assert.ErrorIs(t, err, stellar.ErrContractNotFound)
	})
}

func symbolTopic(t *testing.T, name string) string {
	t.Helper()
	sym := xdr.ScSymbol(name)
	return mustBase64(t, xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym})
}

func executableTopic(t *testing.T, tag string, hash []byte) string {
	t.Helper()
	tagSym := xdr.ScSymbol(tag)
	items := xdr.ScVec{{Type: xdr.ScValTypeScvSymbol, Sym: &tagSym}}
	if hash != nil {
		b := xdr.ScBytes(hash)
		items = append(items, xdr.ScVal{Type: xdr.ScValTypeScvBytes, Bytes: &b})
	}
	vec := &items
	return mustBase64(t, xdr.ScVal{Type: xdr.ScValTypeScvVec, Vec: &vec})
}

func systemEvent(ledger int32, contractID string, topics ...string) map[string]any {
	return map[string]any{
		"type": "system", "ledger": ledger, "contractId": contractID,
		"id": "e", "topic": topics, "value": "AAAAAQ==",
	}
}

func TestExecutableUpdates(t *testing.T) {
	t.Parallel()

	oldHash := make([]byte, 32)
	newHash := make([]byte, 32)
	newHash[0] = 0xab
	ctx := context.Background()

	c := &chainRPC{t: t, events: []map[string]any{
		systemEvent(210, testContractID,
			symbolTopic(t, "executable_update"),
			executableTopic(t, "Wasm", oldHash), executableTopic(t, "Wasm", newHash)),
		// Not an executable update.
		systemEvent(211, testContractID, symbolTopic(t, "something_else"), symbolTopic(t, "x"), symbolTopic(t, "y")),
		// A Stellar asset contract has no WASM to refresh.
		systemEvent(212, testContractID,
			symbolTopic(t, "executable_update"),
			executableTopic(t, "Wasm", oldHash), executableTopic(t, "StellarAsset", nil)),
		// Too few topics.
		systemEvent(213, testContractID, symbolTopic(t, "executable_update")),
	}}
	f := c.start()

	page, err := f.ExecutableUpdates(ctx, stellar.UpdatePosition{StartLedger: 200}, 10)
	require.NoError(t, err)
	require.Len(t, page.Updates, 1)
	assert.Equal(t, stellar.ExecutableUpdate{
		ContractID:  testContractID,
		OldWasmHash: strings.Repeat("00", 32),
		NewWasmHash: "ab" + strings.Repeat("00", 31),
		Ledger:      210,
	}, page.Updates[0])
	assert.Equal(t, "0000000000000000500-0000000000", page.Cursor)
	assert.Equal(t, uint32(500), page.LatestLedger)
	assert.False(t, page.More, "fewer events than the limit means the page is the last")

	t.Run("asks for system events from the start ledger", func(t *testing.T) {
		var params struct {
			StartLedger uint32 `json:"startLedger"`
			Filters     []struct {
				Type string `json:"type"`
			} `json:"filters"`
			Pagination struct {
				Cursor string `json:"cursor"`
				Limit  int    `json:"limit"`
			} `json:"pagination"`
		}
		require.NoError(t, json.Unmarshal(c.requests[len(c.requests)-1].Params, &params))
		assert.Equal(t, uint32(200), params.StartLedger)
		assert.Equal(t, "system", params.Filters[0].Type)
		assert.Equal(t, 10, params.Pagination.Limit)
		assert.Empty(t, params.Pagination.Cursor)
	})

	t.Run("resumes from a cursor without a start ledger", func(t *testing.T) {
		_, err := f.ExecutableUpdates(ctx, stellar.UpdatePosition{Cursor: "0000000000000000500-0000000000"}, 10)
		require.NoError(t, err)
		var params map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(c.requests[len(c.requests)-1].Params, &params))
		assert.NotContains(t, params, "startLedger")
		assert.Contains(t, string(params["pagination"]), "0000000000000000500-0000000000")
	})

	t.Run("a full page says there may be more", func(t *testing.T) {
		page, err := f.ExecutableUpdates(ctx, stellar.UpdatePosition{StartLedger: 200}, 4)
		require.NoError(t, err)
		assert.True(t, page.More)
	})

	t.Run("a position past retention is expired", func(t *testing.T) {
		c.failures = map[string]string{"getEvents": "startLedger must be within the ledger range: 400 - 500"}
		_, err := f.ExecutableUpdates(ctx, stellar.UpdatePosition{StartLedger: 5}, 10)
		assert.ErrorIs(t, err, stellar.ErrPositionExpired)
	})

	t.Run("a mangled cursor is expired, not an error", func(t *testing.T) {
		c.failures = nil
		_, err := f.ExecutableUpdates(ctx, stellar.UpdatePosition{Cursor: "garbage"}, 10)
		assert.ErrorIs(t, err, stellar.ErrPositionExpired)
	})

	t.Run("latest ledger", func(t *testing.T) {
		latest, err := f.LatestLedger(ctx)
		require.NoError(t, err)
		assert.Equal(t, uint32(500), latest)
	})
}
