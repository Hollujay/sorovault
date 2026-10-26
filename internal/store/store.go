// Package store persists the contract registry.
//
// The registry keeps two things per contract: a row tracking its current
// interface version identifier, and one immutable spec row per identifier it
// has ever used. Uploaded contracts use their WASM hash; protocol-provided
// contracts such as SACs use a stable built-in identifier. Refreshing a
// contract therefore appends a version rather than overwriting one.
package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/soroworks/sorovault/internal/model"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("store: not found")

// ErrRegistryNotEmpty is returned by Import when it is asked to load into a
// registry that already holds contracts and merge was not requested.
var ErrRegistryNotEmpty = errors.New("store: registry is not empty")

// SnapshotVersion is the on-disk format version of a Snapshot. Reading a
// snapshot with any other version is refused rather than guessed at.
const SnapshotVersion = 1

// Snapshot is a complete, portable copy of the registry: every contract and
// every interface version it has stored, with the timestamps that make the
// version history meaningful.
type Snapshot struct {
	Version   int                `json:"version"`
	Contracts []ContractSnapshot `json:"contracts"`
}

// ContractSnapshot is one contract together with its version history.
type ContractSnapshot struct {
	Contract Contract       `json:"contract"`
	Versions []SpecSnapshot `json:"versions"`
}

// SpecSnapshot is one stored interface version. The row ID and the search
// symbols are deliberately absent: they are storage details that both
// implementations can rebuild.
type SpecSnapshot struct {
	WasmHash  string           `json:"wasm_hash"`
	Interface *model.Interface `json:"interface"`
	DecodedAt time.Time        `json:"decoded_at"`
}

// Contract is a registered contract's current state.
type Contract struct {
	ContractID      string    `json:"contract_id"`
	Network         string    `json:"network"`
	CurrentWasmHash string    `json:"current_wasm_hash"`
	Name            string    `json:"name,omitempty"`
	SourceURL       string    `json:"source_url,omitempty"`
	SourceCommit    string    `json:"source_commit,omitempty"`
	FirstSeen       time.Time `json:"first_seen"`
	LastRefreshed   time.Time `json:"last_refreshed"`
	// ObservedLedger is only passed to Save for a newly observed version.
	ObservedLedger *uint32 `json:"-"`

	// MissingSince is when a refresh first found the contract absent from
	// the network. It is nil while the contract is live, and is cleared by
	// the next save, which only happens once the contract has been fetched
	// again.
	MissingSince *time.Time `json:"missing_since,omitempty"`

	// Implements lists the standard interface IDs detected from the current
	// decoded interface, such as "sep41".
	Implements []string `json:"implements,omitempty"`

	// DeprecatedSince is when the contract was marked deprecated: it has a
	// replacement and users should stop building on it. It is nil while
	// the contract is in active use, and survives refreshes — being live
	// on the network says nothing about being the recommended one to use.
	DeprecatedSince *time.Time `json:"deprecated_since,omitempty"`

	// Successor is the contract ID that replaces a deprecated one. Empty
	// means the tooling has retired the contract without agreeing where
	// to go.
	Successor string `json:"successor,omitempty"`

	// DeprecationNote says how to move over, from the tooling that marked
	// the contract deprecated.
	DeprecationNote string `json:"deprecation_note,omitempty"`

	// Matches lists the interface symbols — functions, types, events — that
	// a search query matched. It is only set on search results, and is
	// empty when the query matched the contract ID or name instead.
	Matches []string `json:"matches,omitempty"`
}

// symbolSeparator joins symbol names into the single searchable string the
// Postgres store keeps per spec. A newline cannot appear in a symbol, and a
// query cannot contain one once trimmed, so a substring match can never
// straddle two names.
const symbolSeparator = "\n"

// joinSymbols renders an interface's symbol names for storage.
func joinSymbols(iface *model.Interface) string {
	return strings.Join(iface.SymbolNames(), symbolSeparator)
}

// matchSymbols returns the names in a joined symbol string that contain
// query, compared case-insensitively. Both stores use it so they report
// identical matches.
func matchSymbols(joined, query string) []string {
	if query == "" || joined == "" {
		return nil
	}
	needle := strings.ToLower(query)
	var out []string
	for _, name := range strings.Split(joined, symbolSeparator) {
		if strings.Contains(strings.ToLower(name), needle) {
			out = append(out, name)
		}
	}
	return out
}

// joinDocs renders an interface's doc comments for full-text search: the
// docs of every function and user-defined type, newline-separated. Events
// stay out — they are not the callable surface a search over "what does
// this contract do" is about.
func joinDocs(iface *model.Interface) string {
	if iface == nil {
		return ""
	}
	var docs []string
	add := func(doc string) {
		if doc != "" {
			docs = append(docs, doc)
		}
	}
	for _, fn := range iface.Functions {
		add(fn.Doc)
	}
	for _, s := range iface.Types.Structs {
		add(s.Doc)
	}
	for _, u := range iface.Types.Unions {
		add(u.Doc)
	}
	for _, e := range iface.Types.Enums {
		add(e.Doc)
	}
	for _, e := range iface.Types.ErrorEnums {
		add(e.Doc)
	}
	return strings.Join(docs, symbolSeparator)
}

// docTerms splits a query into the terms full-text search matches against:
// lowercased runs of letters and digits, so punctuation, emoji and other
// noise drop out instead of being searched for literally. Splitting on
// punctuation keeps "vault!" and "vault" finding the same docs, and it is
// the part of the matcher both stores must apply the same way — the
// Postgres tsquery is built out of exactly these words.
func docTerms(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// docMatchLower is what docTerms works on: Unicode case folding covers all
// scripts, and lowercasing once here means the term and the doc text go
// through the same fold.
func docMatchLower(text string) []string {
	return docTerms(strings.ToLower(text))
}

// matchDocs reports whether every term of query appears in the joined doc
// blob, as a whole word or by common prefix. An empty blob or a query with
// nothing word-like in it matches nothing.
//
// The prefix slack stands in for the case folding a tsvector applies: a
// query "swap" has to find a doc about "swaps", and "SWAP" about either.
// Both directions of the prefix test matter for that, and they stay in
// step with the Postgres side, which the shared conformance suite pins:
// it builds one lowercased prefix query per term out of the very words
// docMatchLower yields, so a term landing in a doc ballparks its tsquery.
func matchDocs(docs, query string) bool {
	terms := docMatchLower(query)
	if len(terms) == 0 {
		return false
	}
	tokens := docMatchLower(docs)
	for _, term := range terms {
		found := false
		for _, token := range tokens {
			if strings.HasPrefix(token, term) || strings.HasPrefix(term, token) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// hasFunction reports whether an interface declares a function with exactly
// this name.
//
// The match is exact: "transfer" is not "transfer_from" and not
// "Transferable". A caller asking for a function wants the interface to
// implement it, not merely to mention it.
func hasFunction(iface *model.Interface, name string) bool {
	if iface == nil {
		return false
	}
	for _, fn := range iface.Functions {
		if fn.Name == name {
			return true
		}
	}
	return false
}

// hasType reports whether an interface declares a user-defined type with
// exactly this name.
//
// Error enums count: they are types the contract defines, and a client
// matching on a type name means the type it will use.
func hasType(iface *model.Interface, name string) bool {
	if iface == nil {
		return false
	}
	for _, s := range iface.Types.Structs {
		if s.Name == name {
			return true
		}
	}
	for _, u := range iface.Types.Unions {
		if u.Name == name {
			return true
		}
	}
	for _, e := range iface.Types.Enums {
		if e.Name == name {
			return true
		}
	}
	for _, e := range iface.Types.ErrorEnums {
		if e.Name == name {
			return true
		}
	}
	return false
}

// hasAll reports whether an interface declares every requested function and
// type name. The in-memory store uses it; the Postgres one asks the
// database the same question.
func hasAll(iface *model.Interface, functions, types []string) bool {
	for _, name := range functions {
		if !hasFunction(iface, name) {
			return false
		}
	}
	for _, name := range types {
		if !hasType(iface, name) {
			return false
		}
	}
	return true
}

// Spec is one decoded interface version of a contract.
type Spec struct {
	ID         int64            `json:"id"`
	ContractID string           `json:"contract_id"`
	Network    string           `json:"network"`
	WasmHash   string           `json:"wasm_hash"`
	Interface  *model.Interface `json:"interface"`
	DecodedAt  time.Time        `json:"decoded_at"`
	// ObservedLedger is the ledger at observation, not the upgrade ledger.
	ObservedLedger *uint32 `json:"observed_ledger"`
}

// WasmSpec is a decoded interface for uploaded code that no registered
// contract currently runs.
type WasmSpec struct {
	Network   string           `json:"network"`
	WasmHash  string           `json:"wasm_hash"`
	Interface *model.Interface `json:"interface"`
	DecodedAt time.Time        `json:"decoded_at"`
}

// SpecVersion summarises a stored spec without its body, for listing the
// versions of a contract cheaply.
type SpecVersion struct {
	WasmHash    string    `json:"wasm_hash,omitempty"`
	InterfaceID string    `json:"interface_id,omitempty"`
	Builtin     bool      `json:"builtin,omitempty"`
	DecodedAt   time.Time `json:"decoded_at"`
	Current     bool      `json:"current"`
	// ObservedLedger is the ledger at observation, not the upgrade ledger.
	ObservedLedger *uint32 `json:"observed_ledger"`
}

// ListFilter selects and pages a contract listing.
type ListFilter struct {
	// Network restricts results to one network. Empty means all networks.
	Network string
	// Query is a case-insensitive match against contract ID, name, and the
	// names of the functions, types and events in the contract's current
	// interface, and — ranked below those — a whole-word search over the
	// doc comments of its functions and user-defined types, where every
	// word of the query must be found. Empty matches everything.
	Query string
	// Functions restricts results to contracts whose current interface
	// declares a function with exactly this name. Every name must match,
	// so two names ask for a contract with both.
	Functions []string
	// Types restricts results to contracts whose current interface declares
	// a user-defined type with exactly this name. Every name must match.
	Types []string
	// Implements restricts results to contracts implementing every listed standard ID.
	Implements []string
	// Limit is the maximum number of rows to return. Zero means
	// DefaultLimit; anything above MaxLimit is clamped to MaxLimit.
	Limit int
	// Offset is the number of rows to skip. Offset paging is kept for
	// compatibility — it never breaks outright — but cursor paging below
	// is what callers should reach for: offsets grow slower as the
	// registry grows, and rows added while a client pages make offset
	// pages repeat and skip each other. (The Go doc does not carry the
	// formal Deprecated marker, because staticcheck then rejects the
	// compatibility paths that still must honor it — the deprecation is
	// voiced at the API surface instead.)
	Offset int
	// Cursor continues from where a previous page ended. An empty cursor
	// starts from the top, so a first request carries none. A cursor and
	// an offset are exclusive: ask for one page in one of the two ways.
	// A cursor the store cannot read is ErrMalformedCursor.
	Cursor string
}

// Paging bounds for ListFilter.
const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Normalize trims the search term and clamps the filter's paging to the
// supported range. Implementations call it, so callers taking a filter
// straight from user input do not have to.
func (f *ListFilter) Normalize() {
	f.Query = strings.TrimSpace(f.Query)
	// PostgreSQL text parameters cannot contain NUL or invalid UTF-8. Replace
	// either with the Unicode replacement character so both stores search the
	// same normalized query without rejecting arbitrary input.
	f.Query = strings.ToValidUTF8(f.Query, "\uFFFD")
	f.Query = strings.ReplaceAll(f.Query, "\x00", "\uFFFD")
	f.Functions = cleanNames(f.Functions)
	f.Types = cleanNames(f.Types)
	f.Implements = cleanNames(f.Implements)
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// cleanNames trims a list of symbol names and drops the empty ones, so a
// repeated query parameter with no value does not filter everything out.
func cleanNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func hasAllStandards(have, want []string) bool {
	present := make(map[string]struct{}, len(have))
	for _, id := range have {
		present[id] = struct{}{}
	}
	for _, id := range want {
		if _, ok := present[id]; !ok {
			return false
		}
	}
	return true
}

// ContractPage is one page of a contract listing.
type ContractPage struct {
	Contracts []Contract `json:"contracts"`
	// Total is how many contracts match the filter, ignoring paging.
	Total int `json:"total"`
	// Next carries the cursor of the following page when this page did
	// not reach the end of the matches. Empty means the walk is over.
	Next string `json:"next"`
}

// WasmFilter selects the contracts that run a given WASM hash.
//
// Many contracts share one WASM — every instance of a token template, every
// vault a factory deployed — so the question "who else runs this code?" is
// the one a vulnerability in a shared WASM raises.
type WasmFilter struct {
	// Network restricts results to one network. Empty means all networks.
	Network string
	// WasmHash is the hash of the WASM the contracts run.
	WasmHash string
	// Ever includes the contracts that ran the hash in the past, not only
	// the ones running it now.
	Ever bool
	// Limit is the maximum number of rows to return. Zero means
	// DefaultLimit; anything above MaxLimit is clamped to MaxLimit.
	Limit int
	// Offset is the number of rows to skip.
	Offset int
}

// NetworkStats is one network's share of the registry.
type NetworkStats struct {
	Network   string `json:"network"`
	Contracts int    `json:"contracts"`
	Versions  int    `json:"versions"`
}

// Stats summarises the size of the registry.
type Stats struct {
	// Networks is every network with a registered contract, ordered by
	// name.
	Networks []NetworkStats `json:"networks"`
	// Contracts and Versions total every network.
	Contracts int `json:"contracts"`
	Versions  int `json:"versions"`
	// Latest is the most recently registered contract, if the registry has
	// one. Registered means first seen: refreshing an old contract does not
	// make it new.
	Latest *Contract `json:"latest_contract,omitempty"`
}

// Upgrade is one recorded interface upgrade: the version a contract ran
// before, the version it moved to, and when the new one was observed.
//
// It is derived from the specs rows rather than stored separately: any spec
// row beyond a contract's earliest is, by definition, an upgrade.
type Upgrade struct {
	ContractID  string    `json:"contract_id"`
	Network     string    `json:"network"`
	OldWasmHash string    `json:"old_wasm_hash"`
	NewWasmHash string    `json:"new_wasm_hash"`
	ObservedAt  time.Time `json:"observed_at"`
}

// UpgradeFilter selects and pages the upgrade feed.
type UpgradeFilter struct {
	// Network restricts results to one network. Empty means all networks.
	Network string
	// Limit is the maximum number of upgrades to return. Zero means
	// DefaultLimit; anything above MaxLimit is clamped to MaxLimit.
	Limit int
	// Offset is the number of upgrades to skip.
	Offset int
}

// Normalize trims the network and clamps the filter's paging to the
// supported range, as ListFilter.Normalize does.
func (f *WasmFilter) Normalize() {
	f.Network = strings.TrimSpace(f.Network)
	f.WasmHash = strings.TrimSpace(f.WasmHash)
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// Normalize trims the network and clamps the filter's paging to the
// supported range.
func (f *UpgradeFilter) Normalize() {
	f.Network = strings.TrimSpace(f.Network)
	if f.Limit <= 0 {
		f.Limit = DefaultLimit
	}
	if f.Limit > MaxLimit {
		f.Limit = MaxLimit
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
}

// WasmContract is one contract in a listing of the contracts that run a
// given WASM hash.
type WasmContract struct {
	Contract

	// Current is true when the contract runs the hash now, and false when
	// it only ran it in the past.
	Current bool `json:"current"`
}

// WasmContractPage is one page of a listing of the contracts that run a
// given WASM hash.
type WasmContractPage struct {
	Contracts []WasmContract `json:"contracts"`
	// Total is how many contracts match, ignoring paging.
	Total int `json:"total"`
}

// UpgradePage is one page of the upgrade feed.
type UpgradePage struct {
	Upgrades []Upgrade `json:"upgrades"`
	// Total is how many upgrades match the filter, ignoring paging.
	Total int `json:"total"`
}

// Subscription is a per-contract webhook subscription.
type Subscription struct {
	ID          string    `json:"id"`
	Network     string    `json:"network"`
	ContractIDs []string  `json:"contract_ids"`
	URL         string    `json:"url"`
	Secret      string    `json:"-"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
}

// SubscribeRequest is the request body for creating a subscription.
type SubscribeRequest struct {
	ContractIDs []string `json:"contract_ids"`
	URL         string   `json:"url"`
}

// SubscriptionListItem is a summary of a subscription for listing.
type SubscriptionListItem struct {
	ID        string `json:"id"`
	Network   string `json:"network"`
	Contracts int    `json:"contracts"`
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at"`
}

// Store is the registry's persistence layer.
//
// Implementations must be safe for concurrent use. Two are provided: Postgres
// for real deployments, and an in-memory one used by tests.
type Store interface {
	// UpdateSource attaches repository metadata to an existing contract.
	UpdateSource(ctx context.Context, network, contractID, sourceURL, sourceCommit string) error

	// SetDeprecation marks an existing contract deprecated, with an
	// optional successor contract ID and note. A successor the caller must
	// verify — registered on the same network, a contract other than the
	// one being deprecated — is the API's job, not the store's.
	SetDeprecation(ctx context.Context, network, contractID, successor, note string) error

	// ClearDeprecation lifts the mark, restoring the contract to active
	// use.
	ClearDeprecation(ctx context.Context, network, contractID string) error

	// Delete removes a contract and every interface version stored for it.
	// It returns ErrNotFound when the contract is not in the registry.
	Delete(ctx context.Context, network, contractID string) error

	// Export returns every contract and every stored interface version,
	// including their timestamps, for a portable registry backup.
	Export(ctx context.Context) (*Snapshot, error)

	// Import loads a snapshot. With merge false it requires an empty
	// registry; with merge true it adds the contracts and versions that are
	// missing and leaves existing records untouched.
	Import(ctx context.Context, snapshot *Snapshot, merge bool) error

	// SaveWasmSpec records an interface by network and WASM hash, independent
	// of any deployed contract.
	SaveWasmSpec(ctx context.Context, network, wasmHash string, iface *model.Interface) error

	// GetWasmSpec returns an interface for uploaded code by its hash.
	GetWasmSpec(ctx context.Context, network, wasmHash string) (*WasmSpec, error)

	// Save records a decoded interface and points the contract at it.
	//
	// It is idempotent per WASM hash: re-saving a hash already stored for a
	// contract updates the contract's last_refreshed without inserting a
	// duplicate spec row. Saving a new hash appends a version and leaves
	// earlier ones intact.
	Save(ctx context.Context, c Contract, iface *model.Interface) error

	// GetContract returns a contract's current state.
	GetContract(ctx context.Context, network, contractID string) (*Contract, error)

	// MarkMissing records that a contract could not be found on the
	// network, and when it was first noticed to be gone.
	//
	// The mark is not overwritten once it is set: the timestamp says when
	// the contract disappeared, not when it was last looked for. Saving
	// the contract again clears it, since a stored interface means the
	// contract was live.
	MarkMissing(ctx context.Context, network, contractID string, since time.Time) error

	// ListContracts returns a filtered, paged listing.
	ListContracts(ctx context.Context, f ListFilter) (*ContractPage, error)

	// ListContractsByWasm returns a paged listing of the contracts that
	// run the given WASM hash.
	//
	// With Ever set, the listing also covers the contracts that ran the
	// hash in the past and have since been upgraded; each result says
	// whether the contract still runs it.
	ListContractsByWasm(ctx context.Context, f WasmFilter) (*WasmContractPage, error)

	// ListNetworks returns the distinct networks represented in the registry,
	// in ascending order.
	ListNetworks(ctx context.Context) ([]string, error)

	// Stats summarises the registry: contract and version counts per
	// network, and the most recently registered contract.
	Stats(ctx context.Context) (*Stats, error)

	// ListUpgrades returns recent interface upgrades, newest first. A
	// contract's first stored version is its registration, not an upgrade,
	// so it is excluded.
	ListUpgrades(ctx context.Context, f UpgradeFilter) (*UpgradePage, error)

	// GetSpec returns a contract's decoded interface. An empty wasmHash
	// selects whichever version the contract currently runs.
	GetSpec(ctx context.Context, network, contractID, wasmHash string) (*Spec, error)

	// ListVersions returns every stored version of a contract, newest first.
	ListVersions(ctx context.Context, network, contractID string) ([]SpecVersion, error)

	// GetSubscription returns a subscription by its secret (ID).
	GetSubscription(ctx context.Context, secret string) (*Subscription, error)

	// MarkSubscriptionInactive marks a subscription as inactive.
	MarkSubscriptionInactive(ctx context.Context, secret string) error

	// Ping reports whether the store's backing service answers. Readiness
	// uses it; it must not change any stored data.
	Ping(ctx context.Context) error

	// Close releases any resources held by the store.
	Close() error

	// Subscribe creates a new webhook subscription for the given network and
	// contract IDs. Returns the subscription ID (which also serves as the
	// secret for HMAC signing and deletion).
	Subscribe(ctx context.Context, network string, contractIDs []string, url string) (string, error)

	// ListSubscriptions returns all subscriptions, active and inactive.
	ListSubscriptions(ctx context.Context) ([]SubscriptionListItem, error)

	// Unsubscribe removes a subscription by its secret (ID).
	Unsubscribe(ctx context.Context, secret string) error
}

// newSubscriptionSecret returns a random 256-bit subscription ID. The ID is
// also the secret that signs deliveries and authorises deleting the
// subscription, so it must not be guessable.
func newSubscriptionSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("store: generating subscription secret: %w", err)
	}
	return "sub_" + hex.EncodeToString(b[:]), nil
}
