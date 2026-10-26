// Package stellar fetches deployed contract code from a Stellar network.
//
// Reading a contract's WASM takes two RPC round trips. The contract's
// instance ledger entry holds an executable that names the WASM by hash;
// that hash is then the key of the contract code entry holding the bytes
// themselves. Callers that only need to know whether a contract has changed
// can stop after the first trip by calling WasmHash.
package stellar

import (
	"context"
	"errors"
	"fmt"
)

// BuiltinSACIdentifier is a stable interface key, not a WASM hash.
const BuiltinSACIdentifier = "builtin:stellar-asset-contract:v1"

// ErrContractNotFound reports that the network has no live instance entry
// for a contract ID. That covers both a contract that was never deployed and
// one whose entry has expired and not been restored.
var ErrContractNotFound = errors.New("stellar: contract not found on network")

// ErrNoWasm reports that a contract exists but has no WASM to fetch. Stellar
// asset contracts are the usual case: they are built into the host rather
// than uploaded, so they have no interface to decode.
var ErrNoWasm = errors.New("stellar: contract has no uploaded wasm")

// ErrWasmNotFound reports that the network has no uploaded code for a hash.
var ErrWasmNotFound = errors.New("stellar: wasm not found on network")

// ErrRateLimited reports that the RPC endpoint refused a request because
// its rate limit was exceeded.
var ErrRateLimited = errors.New("stellar: RPC rate limit exceeded")

// Contract is a deployed contract's code as fetched from the network.
type Contract struct {
	// ID is the contract's strkey address ("C...").
	ID string
	// WasmHash is the lowercase hex SHA-256 of the WASM, which is also the
	// key its code entry is stored under.
	WasmHash string
	// Wasm is the uploaded module.
	Wasm []byte
	// Builtin is true when the executable is supplied by the Stellar protocol,
	// rather than by uploaded WASM (for example, a Stellar Asset Contract).
	Builtin bool
}

// Fetcher reads deployed contract code from a network.
//
// It is the seam the rest of SoroVault is written against: the registry
// depends on this interface, never on an RPC client, so tests can supply a
// fixture-backed implementation and never touch the network.
type Fetcher interface {
	// FetchContract returns the contract's WASM and the hash it is keyed by.
	FetchContract(ctx context.Context, contractID string) (*Contract, error)

	// FetchWasm returns uploaded code by its SHA-256 hash, whether or not a
	// contract instance currently references it.
	FetchWasm(ctx context.Context, wasmHash string) ([]byte, error)

	// WasmHash returns just the hash the contract currently points at,
	// without downloading the module. Refresh uses it to decide whether
	// there is anything new to decode.
	WasmHash(ctx context.Context, contractID string) (string, error)

	// Network returns the passphrase of the network this fetcher reads,
	// which the registry uses to namespace what it stores.
	Network(ctx context.Context) (string, error)
}

// NetworkName maps a network passphrase to the short label SoroVault stores
// records under. An unrecognised passphrase is returned unchanged so that
// private and future networks still get a stable, distinct namespace.
func NetworkName(passphrase string) string {
	switch passphrase {
	case NetworkPublic:
		return "public"
	case NetworkTestnet:
		return "testnet"
	case NetworkFuturenet:
		return "futurenet"
	case "":
		return "unknown"
	default:
		return passphrase
	}
}

// NetworkLogLabel returns a safe label for logs. NetworkName preserves an
// unrecognised passphrase so private and future networks get distinct storage
// namespaces; that passphrase must not be emitted to logs.
func NetworkLogLabel(network string) string {
	switch network {
	case "public":
		return "public"
	case "testnet":
		return "testnet"
	case "futurenet":
		return "futurenet"
	default:
		return "unknown"
	}
}

// Well-known network passphrases.
const (
	NetworkPublic    = "Public Global Stellar Network ; September 2015"
	NetworkTestnet   = "Test SDF Network ; September 2015"
	NetworkFuturenet = "Test SDF Future Network ; October 2022"
)

// notFoundError wraps ErrContractNotFound with the ID that was missing.
func notFoundError(contractID string) error {
	return fmt.Errorf("%w: %s", ErrContractNotFound, contractID)
}

// ErrOriginUnknown reports that the network's reachable history does not say
// who created a contract. The RPC keeps only a recent window of ledgers, so
// it is the normal answer for an old contract, not a failure.
var ErrOriginUnknown = errors.New("stellar: contract origin is not in the reachable history")

// Origin is where a contract came from.
type Origin struct {
	// Creator is the account that submitted the transaction creating the
	// contract (the operation's source account when it overrides the
	// transaction's). For a contract deployed by a factory it is the
	// account that invoked the factory.
	Creator string
	// Ledger is the sequence of the ledger the contract was created in.
	Ledger uint32
}

// OriginFinder determines a contract's creator and creation ledger.
//
// It is optional: a Fetcher that can also find origins implements it, and
// callers discover it with a type assertion. FindOrigin returns
// ErrOriginUnknown when the history it can reach does not say.
type OriginFinder interface {
	FindOrigin(ctx context.Context, contractID string) (*Origin, error)
}

// ErrPositionExpired reports that the position asked of an UpdateFollower is
// older than the history the network keeps, so upgrades in between were
// missed and cannot be read back.
var ErrPositionExpired = errors.New("stellar: position is older than the network's retained history")

// ExecutableUpdate is one contract replacing the code it runs.
type ExecutableUpdate struct {
	ContractID  string
	OldWasmHash string
	NewWasmHash string
	// Ledger is the ledger the update happened in.
	Ledger uint32
}

// UpdatePosition says where to resume reading upgrade events. A non-empty
// Cursor wins; otherwise reading starts at StartLedger.
type UpdatePosition struct {
	Cursor      string
	StartLedger uint32
}

// UpdatePage is one batch of upgrade events.
type UpdatePage struct {
	Updates []ExecutableUpdate
	// Cursor resumes reading after this page. It advances even when no
	// event matched, so an idle network does not make a reader re-scan.
	Cursor string
	// LatestLedger is the newest ledger the network has closed.
	LatestLedger uint32
	// More is true when the page was full and there may be more events.
	More bool
}

// UpdateFollower reads the system events the network emits when a contract's
// executable is replaced. It is optional, like OriginFinder.
type UpdateFollower interface {
	// LatestLedger returns the newest closed ledger.
	LatestLedger(ctx context.Context) (uint32, error)

	// ExecutableUpdates returns up to limit upgrade events at or after the
	// position. It returns ErrPositionExpired when the position is older
	// than the network's retention.
	ExecutableUpdates(ctx context.Context, from UpdatePosition, limit int) (*UpdatePage, error)
}
