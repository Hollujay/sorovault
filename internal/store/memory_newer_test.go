package store

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/model"
)

// These IDs are ordered lowest to highest, which lets the tie-break tests
// state their expectation in terms of the ordering Postgres uses:
// "ORDER BY first_seen DESC, contract_id".
const (
	newerIDLow   = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN"
	newerIDHigh  = "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
	newerIDLater = "CDZZZZYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
)

// newerTestIface builds the smallest interface the store will accept.
func newerTestIface(fn string) *model.Interface {
	return &model.Interface{Functions: []model.Function{{Name: fn}}}
}

// TestNewerContractOrdersByObservationThenID pins the comparison used to
// choose Stats.Latest. Postgres asks the same question with
// "ORDER BY first_seen DESC, contract_id LIMIT 1", so a later observation
// wins and an equal timestamp is settled by the lower contract ID.
func TestNewerContractOrdersByObservationThenID(t *testing.T) {
	t.Parallel()

	require.Less(t, newerIDLow, newerIDHigh)
	require.Less(t, newerIDHigh, newerIDLater)

	earlier := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	later := earlier.Add(time.Second)

	low := Contract{ContractID: newerIDLow, Network: "testnet", FirstSeen: earlier}
	high := Contract{ContractID: newerIDHigh, Network: "testnet", FirstSeen: earlier}
	latest := Contract{ContractID: newerIDLater, Network: "testnet", FirstSeen: later}

	tests := []struct {
		name   string
		a, b   Contract
		wantAB bool
		wantBA bool
	}{
		{"later observation wins", latest, low, true, false},
		{"earlier observation loses", low, latest, false, true},
		{"equal timestamps prefer lower ID", low, high, true, false},
		{"equal timestamps reject higher ID", high, low, false, true},
		{"the same row is not newer", low, low, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.wantAB, newerContract(tt.a, tt.b))
			assert.Equal(t, tt.wantBA, newerContract(tt.b, tt.a),
				"the answer must be deterministic whichever order the arguments arrive in")
		})
	}
}

// TestMemoryStatsLatestMatchesPostgresOrder exercises the tie-break through
// the store. Contracts registered at the same instant must resolve to the
// same row Postgres's ORDER BY would return, regardless of insertion order.
func TestMemoryStatsLatestMatchesPostgresOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := NewMemory()

	tied := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	m.SetClock(func() time.Time { return tied })

	// Save the higher ID first so insertion order cannot accidentally
	// produce the expected answer.
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDHigh, Network: "testnet", CurrentWasmHash: "hash-high"},
		newerTestIface("high")))
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-low"},
		newerTestIface("low")))

	stats, err := m.Stats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats.Latest)
	assert.Equal(t, newerIDLow, stats.Latest.ContractID,
		"Postgres breaks an equal first_seen tie with the lower contract_id")
	assert.True(t, stats.Latest.FirstSeen.Equal(tied))

	// A genuinely later registration beats the tie-break, even though its
	// ID sorts after both of the tied contracts.
	m.SetClock(func() time.Time { return tied.Add(time.Minute) })
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLater, Network: "testnet", CurrentWasmHash: "hash-later"},
		newerTestIface("later")))

	stats, err = m.Stats(ctx)
	require.NoError(t, err)
	require.NotNil(t, stats.Latest)
	assert.Equal(t, newerIDLater, stats.Latest.ContractID, "a later observation wins")
	assert.True(t, stats.Latest.FirstSeen.After(tied))
}

// TestMemoryTiedVersionsStayConsistentWithGet documents what happens when two
// interface versions are observed at the same instant. The list must contain
// both versions, and the one it marks Current must be exactly what GetSpec
// returns as the current interface, so the two views cannot disagree.
func TestMemoryTiedVersionsStayConsistentWithGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	m := NewMemory()

	tied := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	m.SetClock(func() time.Time { return tied })

	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-v1"},
		newerTestIface("v1")))
	require.NoError(t, m.Save(ctx,
		Contract{ContractID: newerIDLow, Network: "testnet", CurrentWasmHash: "hash-v2"},
		newerTestIface("v2")))

	versions, err := m.ListVersions(ctx, "testnet", newerIDLow)
	require.NoError(t, err)
	require.Len(t, versions, 2, "both observations must be kept when their timestamps tie")

	seen := make(map[string]bool, len(versions))
	var current []string
	for _, version := range versions {
		seen[version.WasmHash] = true
		assert.True(t, version.DecodedAt.Equal(tied), "both versions share the clock's timestamp")
		if version.Current {
			current = append(current, version.WasmHash)
		}
	}
	assert.True(t, seen["hash-v1"] && seen["hash-v2"])
	require.Len(t, current, 1, "exactly one tied version is current")

	got, err := m.GetSpec(ctx, "testnet", newerIDLow, "")
	require.NoError(t, err)
	assert.Equal(t, current[0], got.WasmHash,
		"the version flagged Current must be the interface GetSpec returns")
	assert.Equal(t, "v2", got.Interface.Functions[0].Name,
		"the latest save points the contract at its new interface")
}
