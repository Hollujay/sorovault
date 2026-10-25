package refresher_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/soroworks/sorovault/internal/refresher"
)

// TestSweepDeliversToSubscriptions proves a per-contract subscription hears
// about its own contract's upgrade, signed with its secret, and a
// subscription to another contract hears nothing.
func TestSweepDeliversToSubscriptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rec, url := newRecorder(t)
	other, otherURL := newRecorder(t)
	f := newFixture(t)
	id := contractID(t, 1)
	unrelated := contractID(t, 2)
	f.register(t, id, "v1")
	f.register(t, unrelated, "v1")

	secret, err := f.db.Subscribe(ctx, "testnet", []string{id}, url)
	require.NoError(t, err)
	_, err = f.db.Subscribe(ctx, "testnet", []string{unrelated}, otherURL)
	require.NoError(t, err)

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	f.clock.Advance(interval + time.Minute)

	sum, err := f.refresher(t).Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Changed)

	requests := rec.all()
	require.Len(t, requests, 1, "the subscriber hears about its contract once")
	var change refresher.Change
	require.NoError(t, json.Unmarshal(requests[0].body, &change))
	assert.Equal(t, id, change.ContractID)
	assert.Equal(t, refresher.Sign(secret, requests[0].body), requests[0].signature,
		"deliveries are signed with the subscription's secret")

	assert.Empty(t, other.all(), "a subscription to another contract is not notified")
}

// TestInactiveSubscriptionsAreNotNotified proves a subscription marked
// inactive receives nothing.
func TestInactiveSubscriptionsAreNotNotified(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	rec, url := newRecorder(t)
	f := newFixture(t)
	id := contractID(t, 1)
	f.register(t, id, "v1")

	secret, err := f.db.Subscribe(ctx, "testnet", []string{id}, url)
	require.NoError(t, err)
	require.NoError(t, f.db.MarkSubscriptionInactive(ctx, secret))

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	f.clock.Advance(interval + time.Minute)

	_, err = f.refresher(t).Sweep(ctx)
	require.NoError(t, err)
	assert.Empty(t, rec.all())
}

// TestSubscriptionDeliveryRetriesWithTheBody proves a receiver that fails
// once is retried, and that the retry carries the payload: one request
// cannot be sent twice, so each attempt must build its own.
func TestSubscriptionDeliveryRetriesWithTheBody(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var mu sync.Mutex
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		mu.Lock()
		defer mu.Unlock()
		bodies = append(bodies, body)
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)

	f := newFixture(t)
	id := contractID(t, 1)
	f.register(t, id, "v1")
	_, err := f.db.Subscribe(ctx, "testnet", []string{id}, srv.URL)
	require.NoError(t, err)

	f.fake.Upgrade(id, moduleWith(t, "v2"))
	f.clock.Advance(interval + time.Minute)
	_, err = f.refresher(t).Sweep(ctx)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, bodies, 2, "a 502 is retried once and the retry succeeds")
	assert.NotEmpty(t, bodies[1], "the retry carries the payload")
	assert.Equal(t, bodies[0], bodies[1])
}
