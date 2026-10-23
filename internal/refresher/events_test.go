package refresher_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/refresher"
)

type recordingNotifier struct {
	mu      sync.Mutex
	changes []refresher.Change
}

func (n *recordingNotifier) Notify(_ context.Context, c refresher.Change) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changes = append(n.changes, c)
	return nil
}

func (n *recordingNotifier) seen() []refresher.Change {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]refresher.Change(nil), n.changes...)
}

// eventRefresher builds a refresher that follows the fixture's fake network.
func (f *fixture) eventRefresher(t *testing.T, notifier refresher.Notifier) *refresher.Refresher {
	t.Helper()
	r, err := refresher.New(f.reg, refresher.Options{
		Interval:       interval,
		Logger:         f.log,
		Notifier:       notifier,
		Now:            f.clock.Now,
		Events:         f.fake,
		EventsInterval: 5 * time.Millisecond,
		Wait:           func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	require.NoError(t, err)
	return r
}

func (f *fixture) hash(t *testing.T, id string) string {
	t.Helper()
	c, err := f.db.GetContract(context.Background(), "testnet", id)
	require.NoError(t, err)
	return c.CurrentWasmHash
}

func TestPollEventsRefreshesUpgradedContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	notifier := &recordingNotifier{}
	r := f.eventRefresher(t, notifier)

	id := contractID(t, 1)
	f.register(t, id, "v1")
	oldHash := f.hash(t, id)

	// With no saved position the first poll cannot know what it missed, so
	// it sweeps everything and starts reading events from here.
	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Checked, "the first poll is a full sweep")
	assert.Zero(t, sum.Changed)
	saved, err := f.db.GetState(ctx, "refresher.events.testnet")
	require.NoError(t, err)
	assert.Contains(t, saved, "ledger:")

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	f.fake.Calls = map[string]int{}

	sum, err = r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Checked)
	assert.Equal(t, 1, sum.Changed)
	assert.NotEqual(t, oldHash, f.hash(t, id), "the contract was refreshed without waiting for a sweep")
	assert.Equal(t, 1, f.fake.Calls["FetchContract"], "only the upgraded contract was downloaded")

	changes := notifier.seen()
	require.Len(t, changes, 1)
	assert.Equal(t, id, changes[0].ContractID)
	assert.Equal(t, oldHash, changes[0].OldWasmHash)

	t.Run("an event already handled is not handled again", func(t *testing.T) {
		sum, err := r.PollEvents(ctx)
		require.NoError(t, err)
		assert.Zero(t, sum.Checked)
		assert.Len(t, notifier.seen(), 1)
	})
}

func TestPollEventsIgnoresContractsOutsideTheRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := f.eventRefresher(t, nil)

	_, err := r.PollEvents(ctx) // establish a position on an empty registry
	require.NoError(t, err)

	stranger := contractID(t, 9)
	f.fake.Deploy(stranger, moduleWith(t, "v1"))
	f.fake.Upgrade(stranger, moduleWith(t, "v2"))
	f.fake.Calls = map[string]int{}

	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Zero(t, sum.Checked)
	assert.Zero(t, f.fake.Calls["WasmHash"]+f.fake.Calls["FetchContract"], "no RPC work for a contract nobody registered")
}

func TestPollEventsRefreshesAContractOncePerPage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := f.eventRefresher(t, nil)

	id := contractID(t, 1)
	f.register(t, id, "v1")
	_, err := r.PollEvents(ctx)
	require.NoError(t, err)

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	f.fake.Upgrade(id, moduleWith(t, "v3"))

	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Checked)
	assert.Equal(t, 1, sum.Changed)
}

func TestPollEventsSkipsWhatASweepAlreadyRefreshed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := f.eventRefresher(t, nil)

	id := contractID(t, 1)
	f.register(t, id, "v1")
	_, err := r.PollEvents(ctx)
	require.NoError(t, err)

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	_, err = f.reg.Refresh(ctx, id) // the polling safety net got there first
	require.NoError(t, err)

	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Zero(t, sum.Checked)
}

func TestPollEventsResumesFromTheSavedPosition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)

	id := contractID(t, 1)
	f.register(t, id, "v1")
	first := f.eventRefresher(t, nil)
	_, err := first.PollEvents(ctx)
	require.NoError(t, err)

	// An upgrade lands while the process is down.
	f.fake.Upgrade(id, moduleWith(t, "v2"))

	// A new refresher over the same store picks up where the old one left off.
	restarted := f.eventRefresher(t, nil)
	sum, err := restarted.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Changed, "the upgrade that happened during the restart is not lost")
	assert.Zero(t, f.fake.Calls["LatestLedger"]-1, "a restart does not resync when it has a position")
}

func TestPollEventsFallsBackToASweepWhenTheRangeWasMissed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := f.eventRefresher(t, nil)

	a, b := contractID(t, 1), contractID(t, 2)
	f.register(t, a, "v1")
	f.register(t, b, "v1")
	_, err := r.PollEvents(ctx)
	require.NoError(t, err)

	// Both contracts are upgraded, then the network forgets the events.
	f.fake.Upgrade(a, moduleWith(t, "v2"))
	f.fake.Upgrade(b, moduleWith(t, "v2"))
	f.fake.ExpireEvents()
	hashA, hashB := f.hash(t, a), f.hash(t, b)

	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, sum.Checked, "every contract is checked, not only those with a surviving event")
	assert.Equal(t, 2, sum.Changed)
	assert.NotEqual(t, hashA, f.hash(t, a))
	assert.NotEqual(t, hashB, f.hash(t, b))

	t.Run("and carries on from a fresh position", func(t *testing.T) {
		f.fake.Upgrade(a, moduleWith(t, "v3"))
		sum, err := r.PollEvents(ctx)
		require.NoError(t, err)
		assert.Equal(t, 1, sum.Changed)
		assert.Equal(t, 1, sum.Checked)
	})
}

func TestPollEventsKeepsItsPositionWhenTheSourceFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFixture(t)
	r := f.eventRefresher(t, nil)

	id := contractID(t, 1)
	f.register(t, id, "v1")
	_, err := r.PollEvents(ctx)
	require.NoError(t, err)
	f.fake.Upgrade(id, moduleWith(t, "v2"))

	f.fake.EventsErr = errors.New("rpc down")
	_, err = r.PollEvents(ctx)
	require.Error(t, err)

	f.fake.EventsErr = nil
	sum, err := r.PollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Changed, "the event is still there once the source recovers")
}

func TestPollEventsWithoutASource(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.refresher(t).PollEvents(context.Background())
	assert.Error(t, err)
}

func TestRunFollowsEventsAlongsidePolling(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	notifier := &recordingNotifier{}
	r := f.eventRefresher(t, notifier)

	id := contractID(t, 1)
	f.register(t, id, "v1")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for the follower to establish its position, then upgrade.
	require.Eventually(t, func() bool {
		_, err := f.db.GetState(context.Background(), "refresher.events.testnet")
		return err == nil
	}, 5*time.Second, 5*time.Millisecond)
	f.fake.Upgrade(id, moduleWith(t, "v2"))

	require.Eventually(t, func() bool { return len(notifier.seen()) == 1 }, 5*time.Second, 5*time.Millisecond,
		"the upgrade is noticed long before the next scheduled sweep")

	cancel()
	require.NoError(t, <-done)
}
