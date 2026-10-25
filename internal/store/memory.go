package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/soroworks/sorovault/internal/model"
	"github.com/soroworks/sorovault/internal/spec"
)

// Memory is an in-process Store.
//
// It exists so the API, CLI and browse UI can be tested without a database,
// and so `sorovault` can be run against a throwaway registry. It is not
// durable: everything is lost when the process exits.
type Memory struct {
	mu        sync.RWMutex
	contracts map[string]Contract
	specs     map[string][]Spec
	wasmSpecs map[string]WasmSpec
	state     map[string]string
	subs      map[string]Subscription
	nextID    int64

	// now is the clock, swappable so tests can produce deterministic and
	// strictly ordered timestamps.
	now func() time.Time
}

var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory {
	return &Memory{
		contracts: make(map[string]Contract),
		specs:     make(map[string][]Spec),
		wasmSpecs: make(map[string]WasmSpec),
		state:     make(map[string]string),
		subs:      make(map[string]Subscription),
		now:       time.Now,
	}
}

// SetClock replaces the store's clock. Tests use it to make timestamps
// predictable; production code should leave the default alone.
func (m *Memory) SetClock(now func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = now
}

// Ping satisfies Store. The memory store has no external service to ask.
func (m *Memory) Ping(ctx context.Context) error { return ctx.Err() }

// Close satisfies Store. There is nothing to release.
func (m *Memory) Close() error { return nil }

// Subscribe creates a new webhook subscription for the given network and
// contract IDs. Returns the subscription ID (which also serves as the
// secret for HMAC signing and deletion).
func (m *Memory) Subscribe(ctx context.Context, network string, contractIDs []string, url string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	id, err := newSubscriptionSecret()
	if err != nil {
		return "", err
	}
	k := id
	sub := Subscription{
		ID:          id,
		Network:     network,
		ContractIDs: contractIDs,
		URL:         url,
		Active:      true,
		CreatedAt:   m.now(),
	}
	m.subs[k] = sub
	return id, nil
}

// ListSubscriptions returns all subscriptions, active and inactive.
func (m *Memory) ListSubscriptions(ctx context.Context) ([]SubscriptionListItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]SubscriptionListItem, 0, len(m.subs))
	for _, sub := range m.subs {
		items = append(items, SubscriptionListItem{
			ID:        sub.ID,
			Network:   sub.Network,
			Contracts: len(sub.ContractIDs),
			Active:    sub.Active,
			CreatedAt: sub.CreatedAt.UTC().Format(time.RFC3339),
		})
	}
	// Sort by created_at descending
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt > items[j].CreatedAt
	})
	return items, nil
}

// GetSubscription returns a subscription by its secret (ID).
func (m *Memory) GetSubscription(ctx context.Context, secret string) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	k := secret
	sub, ok := m.subs[k]
	if !ok {
		return nil, fmt.Errorf("%w: subscription not found", ErrNotFound)
	}
	// Return a clone so the caller can mutate it safely.
	out := sub
	out.ContractIDs = make([]string, len(sub.ContractIDs))
	copy(out.ContractIDs, sub.ContractIDs)
	return &out, nil
}

// MarkSubscriptionInactive marks a subscription as inactive.
func (m *Memory) MarkSubscriptionInactive(ctx context.Context, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := secret
	sub, ok := m.subs[k]
	if !ok {
		return fmt.Errorf("%w: subscription not found", ErrNotFound)
	}
	sub.Active = false
	m.subs[k] = sub
	return nil
}

// Unsubscribe removes a subscription by its secret (ID).
func (m *Memory) Unsubscribe(ctx context.Context, secret string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := secret
	if _, ok := m.subs[k]; !ok {
		return fmt.Errorf("%w: subscription not found", ErrNotFound)
	}
	delete(m.subs, k)
	return nil
}

func key(network, contractID string) string { return network + "\x00" + contractID }

func wasmSpecKey(network, wasmHash string) string { return network + "\x00" + wasmHash }

// SaveWasmSpec stores an interface for uploaded code independent of contracts.
func (m *Memory) SaveWasmSpec(ctx context.Context, network, wasmHash string, iface *model.Interface) error {
	if iface == nil {
		return errors.New("store: cannot save a nil interface")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cloned, err := cloneInterface(iface)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wasmSpecs[wasmSpecKey(network, wasmHash)] = WasmSpec{
		Network: network, WasmHash: wasmHash, Interface: cloned, DecodedAt: m.now(),
	}
	return nil
}

// GetWasmSpec returns a hash-addressed interface without requiring a contract.
func (m *Memory) GetWasmSpec(ctx context.Context, network, wasmHash string) (*WasmSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.wasmSpecs[wasmSpecKey(network, wasmHash)]
	if !ok {
		return nil, fmt.Errorf("%w: wasm spec %s on %s", ErrNotFound, wasmHash, network)
	}
	cloned, err := cloneInterface(s.Interface)
	if err != nil {
		return nil, err
	}
	s.Interface = cloned
	return &s, nil
}

// Save records a decoded interface and points the contract at it, matching
// the Postgres implementation's append-don't-overwrite semantics.
func (m *Memory) Save(ctx context.Context, c Contract, iface *model.Interface) error {
	if iface == nil {
		return errors.New("store: cannot save a nil interface")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	// Round-tripping through JSON matches what Postgres does to a spec on
	// the way in and out, so tests cannot come to depend on a shared
	// pointer that the real store would never hand back.
	cloned, err := cloneInterface(iface)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	k := key(c.Network, c.ContractID)

	existing, found := m.contracts[k]
	if found {
		c.FirstSeen = existing.FirstSeen
		if c.Name == "" {
			c.Name = existing.Name
		}
		if c.SourceURL == "" {
			c.SourceURL = existing.SourceURL
		}
		if c.SourceCommit == "" {
			c.SourceCommit = existing.SourceCommit
		}
		// Saving an interface says nothing about who made the contract or
		// who vouched for it, so both survive.
		if c.Creator == "" {
			c.Creator, c.CreatedLedger = existing.Creator, existing.CreatedLedger
		}
		c.VerifiedBy, c.VerifiedWasmHash = existing.VerifiedBy, existing.VerifiedWasmHash
		c.VerifiedAt = copyMissing(existing.VerifiedAt)
	} else {
		c.FirstSeen = now
	}
	c.LastRefreshed = now
	// A saved interface means the contract was live, so it is not missing
	// any more.
	c.Implements = spec.ImplementedStandards(iface)
	c.MissingSince = nil
	m.contracts[k] = c

	for i := range m.specs[k] {
		if m.specs[k][i].WasmHash == c.CurrentWasmHash {
			m.specs[k][i].Interface = cloned
			return nil
		}
	}

	m.nextID++
	m.specs[k] = append(m.specs[k], Spec{
		ID:             m.nextID,
		Network:        c.Network,
		ContractID:     c.ContractID,
		WasmHash:       c.CurrentWasmHash,
		Interface:      cloned,
		DecodedAt:      now,
		ObservedLedger: c.ObservedLedger,
	})
	return nil
}

// MarkMissing records that a contract could not be found on the network.
func (m *Memory) MarkMissing(ctx context.Context, network, contractID string, since time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok {
		return fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	// The first sighting is the one that matters: a contract that stays
	// missing keeps the timestamp of when it went away.
	if c.MissingSince == nil {
		stamp := since.UTC()
		c.MissingSince = &stamp
		m.contracts[k] = c
	}
	return nil
}

// UpdateSource attaches repository metadata to an existing contract.
func (m *Memory) UpdateSource(ctx context.Context, network, contractID, sourceURL, sourceCommit string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok {
		return fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	c.SourceURL = sourceURL
	c.SourceCommit = sourceCommit
	m.contracts[k] = c
	return nil
}

// SetOrigin records who created a contract and in which ledger.
func (m *Memory) SetOrigin(ctx context.Context, network, contractID, creator string, ledger uint32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok {
		return fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	c.Creator, c.CreatedLedger = creator, ledger
	m.contracts[k] = c
	return nil
}

// SetVerified records an accepted publisher claim.
func (m *Memory) SetVerified(ctx context.Context, network, contractID, name, sourceURL, sourceCommit string, v Verification) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok {
		return fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	at := v.VerifiedAt.UTC()
	c.Name, c.SourceURL, c.SourceCommit = name, sourceURL, sourceCommit
	c.VerifiedBy, c.VerifiedAt, c.VerifiedWasmHash = v.Publisher, &at, v.WasmHash
	m.contracts[k] = c
	return nil
}

// GetState returns a persisted value.
func (m *Memory) GetState(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	value, ok := m.state[key]
	if !ok {
		return "", fmt.Errorf("%w: state %q", ErrNotFound, key)
	}
	return value, nil
}

// SetState stores a value under key.
func (m *Memory) SetState(ctx context.Context, key, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state[key] = value
	return nil
}

// Delete removes a contract and every interface version stored for it.
func (m *Memory) Delete(ctx context.Context, network, contractID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	k := key(network, contractID)
	if _, ok := m.contracts[k]; !ok {
		return fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	delete(m.contracts, k)
	delete(m.specs, k)
	return nil
}

// Export returns a stable, deep copy of every contract and version.
func (m *Memory) Export(ctx context.Context) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshot := &Snapshot{
		Version:   SnapshotVersion,
		Contracts: make([]ContractSnapshot, 0, len(m.contracts)),
	}
	for k, c := range m.contracts {
		cs := ContractSnapshot{Contract: c}
		cs.Contract.MissingSince = copyMissing(c.MissingSince)
		cs.Contract.VerifiedAt = copyMissing(c.VerifiedAt)
		cs.Contract.Matches = nil
		for _, spec := range m.specs[k] {
			cloned, err := cloneInterface(spec.Interface)
			if err != nil {
				return nil, err
			}
			cs.Versions = append(cs.Versions, SpecSnapshot{
				WasmHash:  spec.WasmHash,
				Interface: cloned,
				DecodedAt: spec.DecodedAt,
			})
		}
		sort.Slice(cs.Versions, func(i, j int) bool {
			if !cs.Versions[i].DecodedAt.Equal(cs.Versions[j].DecodedAt) {
				return cs.Versions[i].DecodedAt.Before(cs.Versions[j].DecodedAt)
			}
			return cs.Versions[i].WasmHash < cs.Versions[j].WasmHash
		})
		snapshot.Contracts = append(snapshot.Contracts, cs)
	}

	sort.Slice(snapshot.Contracts, func(i, j int) bool {
		if snapshot.Contracts[i].Contract.Network != snapshot.Contracts[j].Contract.Network {
			return snapshot.Contracts[i].Contract.Network < snapshot.Contracts[j].Contract.Network
		}
		return snapshot.Contracts[i].Contract.ContractID < snapshot.Contracts[j].Contract.ContractID
	})
	return snapshot, nil
}

// Import loads a snapshot into the memory store, preserving timestamps.
func (m *Memory) Import(ctx context.Context, snapshot *Snapshot, merge bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}

	prepared := make([]ContractSnapshot, 0, len(snapshot.Contracts))
	for _, cs := range snapshot.Contracts {
		cs.Contract.MissingSince = copyMissing(cs.Contract.MissingSince)
		cs.Contract.VerifiedAt = copyMissing(cs.Contract.VerifiedAt)
		cs.Contract.Matches = nil
		for i := range cs.Versions {
			cloned, err := cloneInterface(cs.Versions[i].Interface)
			if err != nil {
				return err
			}
			cs.Versions[i].Interface = cloned
		}
		prepared = append(prepared, cs)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if !merge && len(m.contracts) > 0 {
		return ErrRegistryNotEmpty
	}

	for _, cs := range prepared {
		k := key(cs.Contract.Network, cs.Contract.ContractID)
		if _, exists := m.contracts[k]; !exists {
			m.contracts[k] = cs.Contract
		}
		for _, version := range cs.Versions {
			if m.hasVersion(k, version.WasmHash) {
				continue
			}
			m.nextID++
			m.specs[k] = append(m.specs[k], Spec{
				ID:         m.nextID,
				Network:    cs.Contract.Network,
				ContractID: cs.Contract.ContractID,
				WasmHash:   version.WasmHash,
				Interface:  version.Interface,
				DecodedAt:  version.DecodedAt,
			})
		}
	}
	return nil
}

// hasVersion reports whether a contract already has a stored version for a
// WASM hash. The caller holds m.mu.
func (m *Memory) hasVersion(k, wasmHash string) bool {
	for _, spec := range m.specs[k] {
		if spec.WasmHash == wasmHash {
			return true
		}
	}
	return false
}

// validateSnapshot checks the parts of a snapshot that every store must
// agree on before writing anything.
func validateSnapshot(snapshot *Snapshot) error {
	if snapshot == nil {
		return errors.New("store: cannot import a nil snapshot")
	}
	if snapshot.Version != SnapshotVersion {
		return fmt.Errorf("store: unsupported snapshot version %d (want %d)",
			snapshot.Version, SnapshotVersion)
	}
	for _, cs := range snapshot.Contracts {
		if cs.Contract.Network == "" || cs.Contract.ContractID == "" {
			return errors.New("store: snapshot contains a contract without a network or contract ID")
		}
		for _, version := range cs.Versions {
			if version.Interface == nil {
				return fmt.Errorf("store: snapshot for %s has a version without an interface",
					cs.Contract.ContractID)
			}
		}
	}
	return nil
}

// GetContract returns one contract's current state.
func (m *Memory) GetContract(ctx context.Context, network, contractID string) (*Contract, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	c, ok := m.contracts[key(network, contractID)]
	if !ok {
		return nil, fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}
	c.MissingSince = copyMissing(c.MissingSince)
	c.VerifiedAt = copyMissing(c.VerifiedAt)
	return &c, nil
}

// copyMissing returns an independent copy of a missing mark, so that a
// caller cannot change what the store holds through the pointer.
func copyMissing(since *time.Time) *time.Time {
	if since == nil {
		return nil
	}
	out := *since
	return &out
}

// ListNetworks returns the distinct networks represented in the registry.
func (m *Memory) ListNetworks(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	seen := make(map[string]struct{})
	for _, contract := range m.contracts {
		seen[contract.Network] = struct{}{}
	}
	networks := make([]string, 0, len(seen))
	for network := range seen {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	return networks, nil
}

// Stats summarises the registry from its in-memory rows.
func (m *Memory) Stats(ctx context.Context) (*Stats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	byNetwork := make(map[string]*NetworkStats)
	stats := &Stats{Networks: []NetworkStats{}}
	for k, c := range m.contracts {
		entry, ok := byNetwork[c.Network]
		if !ok {
			entry = &NetworkStats{Network: c.Network}
			byNetwork[c.Network] = entry
		}
		entry.Contracts++
		entry.Versions += len(m.specs[k])
		stats.Contracts++
		stats.Versions += len(m.specs[k])

		if stats.Latest == nil || newerContract(c, *stats.Latest) {
			latest := c
			latest.MissingSince = copyMissing(c.MissingSince)
			latest.VerifiedAt = copyMissing(c.VerifiedAt)
			stats.Latest = &latest
		}
	}

	networks := make([]string, 0, len(byNetwork))
	for network := range byNetwork {
		networks = append(networks, network)
	}
	sort.Strings(networks)
	for _, network := range networks {
		stats.Networks = append(stats.Networks, *byNetwork[network])
	}
	return stats, nil
}

// newerContract reports whether a was registered more recently than b. The
// contract ID breaks a tie so the answer does not depend on map order.
func newerContract(a, b Contract) bool {
	if !a.FirstSeen.Equal(b.FirstSeen) {
		return a.FirstSeen.After(b.FirstSeen)
	}
	return a.ContractID < b.ContractID
}

// ListUpgrades returns recorded interface upgrades, newest first.
func (m *Memory) ListUpgrades(ctx context.Context, f UpgradeFilter) (*UpgradePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.Normalize()

	m.mu.RLock()
	defer m.mu.RUnlock()

	var upgrades []Upgrade
	for k, specs := range m.specs {
		c, ok := m.contracts[k]
		if !ok || (f.Network != "" && c.Network != f.Network) {
			continue
		}
		// Save appends a spec only when a contract starts running a hash it
		// has not stored before, so this list is already in observation
		// order. The first entry is the registration, not an upgrade.
		for i := 1; i < len(specs); i++ {
			upgrades = append(upgrades, Upgrade{
				ContractID:  c.ContractID,
				Network:     c.Network,
				OldWasmHash: specs[i-1].WasmHash,
				NewWasmHash: specs[i].WasmHash,
				ObservedAt:  specs[i].DecodedAt,
			})
		}
	}

	sort.Slice(upgrades, func(i, j int) bool {
		if !upgrades[i].ObservedAt.Equal(upgrades[j].ObservedAt) {
			return upgrades[i].ObservedAt.After(upgrades[j].ObservedAt)
		}
		if upgrades[i].ContractID != upgrades[j].ContractID {
			return upgrades[i].ContractID < upgrades[j].ContractID
		}
		return upgrades[i].NewWasmHash < upgrades[j].NewWasmHash
	})

	page := &UpgradePage{Total: len(upgrades), Upgrades: []Upgrade{}}
	if f.Offset < len(upgrades) {
		end := min(f.Offset+f.Limit, len(upgrades))
		page.Upgrades = append(page.Upgrades, upgrades[f.Offset:end]...)
	}
	return page, nil
}

// ListContracts returns a filtered, paged listing ordered newest-refreshed
// first.
func (m *Memory) ListContracts(ctx context.Context, f ListFilter) (*ContractPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.Normalize()
	if f.Cursor != "" && f.Offset != 0 {
		return nil, errors.New("store: a cursor and an offset are exclusive")
	}
	var cur listCursor
	if f.Cursor != "" {
		var err error
		cur, err = decodeListCursor(f.Cursor)
		if err != nil {
			return nil, err
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	matched := make([]Contract, 0, len(m.contracts))
	needle := strings.ToLower(f.Query)
	for k, c := range m.contracts {
		if f.Network != "" && c.Network != f.Network {
			continue
		}
		if len(f.Implements) > 0 && !hasAllStandards(c.Implements, f.Implements) {
			continue
		}
		if needle != "" {
			c.Matches = matchSymbols(m.currentSymbols(k, c.CurrentWasmHash), f.Query)
			if len(c.Matches) == 0 &&
				!strings.Contains(strings.ToLower(c.ContractID), needle) &&
				!strings.Contains(strings.ToLower(c.Name), needle) {
				continue
			}
		}
		if len(f.Functions) > 0 || len(f.Types) > 0 {
			if !hasAll(m.currentInterface(k, c.CurrentWasmHash), f.Functions, f.Types) {
				continue
			}
		}
		c.MissingSince = copyMissing(c.MissingSince)
		c.VerifiedAt = copyMissing(c.VerifiedAt)
		matched = append(matched, c)
	}

	sort.Slice(matched, func(i, j int) bool { return contractListBefore(matched[i], matched[j]) })

	// A cursor pages by key: the next page starts at the first row that
	// sorts strictly after the one the cursor names. The slice is already
	// in listing order, so that is one binary search, not a scan.
	start := 0
	if cur.ContractID != "" {
		start = sort.Search(len(matched), func(i int) bool {
			return afterListCursor(matched[i], cur)
		})
	} else {
		start = f.Offset
	}

	page := &ContractPage{Total: len(matched), Contracts: []Contract{}}
	if start < len(matched) {
		end := min(start+f.Limit, len(matched))
		page.Contracts = append(page.Contracts, matched[start:end]...)
		if end < len(matched) {
			next, err := encodeListCursor(matched[end-1])
			if err != nil {
				return nil, err
			}
			page.Next = next
		}
	}
	return page, nil
}

// contractListBefore is the listing's total order: last_refreshed
// descending, then contract ID and network ascending. Sorting and cursor
// paging share it, so a cursor walk and the order its pages arrive in are
// the same thing rather than two conventions that agree by luck.
func contractListBefore(a, b Contract) bool {
	if !a.LastRefreshed.Equal(b.LastRefreshed) {
		return a.LastRefreshed.After(b.LastRefreshed)
	}
	if a.ContractID != b.ContractID {
		return a.ContractID < b.ContractID
	}
	return a.Network < b.Network
}

// ListContractsByWasm returns a paged listing of the contracts that run the
// given WASM hash, and — with Ever set — of the ones that ran it in the
// past.
func (m *Memory) ListContractsByWasm(ctx context.Context, f WasmFilter) (*WasmContractPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.Normalize()

	m.mu.RLock()
	defer m.mu.RUnlock()

	matched := make([]WasmContract, 0, len(m.contracts))
	for k, c := range m.contracts {
		if f.Network != "" && c.Network != f.Network {
			continue
		}
		current := c.CurrentWasmHash == f.WasmHash
		if !current {
			if !f.Ever || !m.ranWasm(k, f.WasmHash) {
				continue
			}
		}
		c.MissingSince = copyMissing(c.MissingSince)
		c.VerifiedAt = copyMissing(c.VerifiedAt)
		matched = append(matched, WasmContract{Contract: c, Current: current})
	}

	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].LastRefreshed.Equal(matched[j].LastRefreshed) {
			return matched[i].LastRefreshed.After(matched[j].LastRefreshed)
		}
		return matched[i].ContractID < matched[j].ContractID
	})

	page := &WasmContractPage{Contracts: []WasmContract{}}
	if f.Offset < len(matched) {
		end := min(f.Offset+f.Limit, len(matched))
		page.Contracts = append(page.Contracts, matched[f.Offset:end]...)
	}
	page.Total = len(matched)
	return page, nil
}

// ranWasm reports whether a contract has a stored interface for the given
// WASM hash, which is how the store knows it ran that code at some point.
// The caller holds m.mu.
func (m *Memory) ranWasm(k, wasmHash string) bool {
	for _, s := range m.specs[k] {
		if s.WasmHash == wasmHash {
			return true
		}
	}
	return false
}

// currentSymbols returns the joined symbol names of the spec a contract
// currently runs. The caller holds m.mu.
func (m *Memory) currentSymbols(k, wasmHash string) string {
	for _, s := range m.specs[k] {
		if s.WasmHash == wasmHash {
			return joinSymbols(s.Interface)
		}
	}
	return ""
}

// currentInterface returns the decoded interface of the spec a contract
// currently runs. The caller holds m.mu.
func (m *Memory) currentInterface(k, wasmHash string) *model.Interface {
	for _, s := range m.specs[k] {
		if s.WasmHash == wasmHash {
			return s.Interface
		}
	}
	return nil
}

// GetSpec returns a stored interface, defaulting to the contract's current
// version when wasmHash is empty.
func (m *Memory) GetSpec(ctx context.Context, network, contractID, wasmHash string) (*Spec, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok {
		return nil, specNotFound(network, contractID, wasmHash)
	}

	want := wasmHash
	if want == "" {
		want = c.CurrentWasmHash
	}

	for i := range m.specs[k] {
		if m.specs[k][i].WasmHash == want {
			s := m.specs[k][i]
			cloned, err := cloneInterface(s.Interface)
			if err != nil {
				return nil, err
			}
			s.Interface = cloned
			return &s, nil
		}
	}
	return nil, specNotFound(network, contractID, wasmHash)
}

// ListVersions returns every stored version of a contract, newest first.
func (m *Memory) ListVersions(ctx context.Context, network, contractID string) ([]SpecVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	k := key(network, contractID)
	c, ok := m.contracts[k]
	if !ok || len(m.specs[k]) == 0 {
		return nil, fmt.Errorf("%w: contract %s on %s", ErrNotFound, contractID, network)
	}

	versions := make([]SpecVersion, 0, len(m.specs[k]))
	for _, s := range m.specs[k] {
		v := SpecVersion{DecodedAt: s.DecodedAt, Current: s.WasmHash == c.CurrentWasmHash, ObservedLedger: s.ObservedLedger}
		if s.WasmHash == model.StellarAssetContractIdentifier {
			v.InterfaceID = s.WasmHash
			v.Builtin = true
		} else {
			v.WasmHash = s.WasmHash
		}
		versions = append(versions, v)
	}

	sort.Slice(versions, func(i, j int) bool {
		return versions[i].DecodedAt.After(versions[j].DecodedAt)
	})
	return versions, nil
}

// cloneInterface deep-copies an interface via its JSON encoding, which is
// also how it is stored, so a caller can never mutate what the store holds.
func cloneInterface(iface *model.Interface) (*model.Interface, error) {
	if iface == nil {
		return nil, errors.New("store: cannot clone a nil interface")
	}
	body, err := json.Marshal(iface)
	if err != nil {
		return nil, fmt.Errorf("store: encoding interface: %w", err)
	}
	var out model.Interface
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("store: decoding interface: %w", err)
	}
	return &out, nil
}
