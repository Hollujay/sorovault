package refresher

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// waitLimit is how long a wait that should return immediately may take. It
// is deliberately generous: the test cares that the wait does not run for
// the requested delay, not about microsecond scheduling.
const waitLimit = time.Second

// longDelay is far longer than any test should wait. A regression that
// ignores cancellation makes the test fail after this delay rather than
// hanging until the test binary times out.
const longDelay = 5 * time.Second

// TestWaitContextReturnsTheContextError proves cancellation interrupts a
// wait instead of letting it run its full duration.
func TestWaitContextReturnsTheContextError(t *testing.T) {
	t.Parallel()

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		start := time.Now()
		err := waitContext(ctx, longDelay)
		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.Canceled)
		assert.Less(t, elapsed, waitLimit, "a cancelled wait must return immediately")
	})

	t.Run("deadline exceeded", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		start := time.Now()
		err := waitContext(ctx, longDelay)
		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, elapsed, waitLimit, "an expired wait must return immediately")
	})
}

// TestWaitContextSleepsForTheDuration proves an un-cancelled wait returns nil
// once the requested duration has elapsed.
func TestWaitContextSleepsForTheDuration(t *testing.T) {
	t.Parallel()

	const delay = 25 * time.Millisecond

	start := time.Now()
	err := waitContext(context.Background(), delay)
	elapsed := time.Since(start)

	require.NoError(t, err)
	assert.GreaterOrEqual(t, elapsed, delay, "the wait must not return before the timer elapses")
	assert.Less(t, elapsed, waitLimit)
}

// TestWaitContextZeroAndNegativeReturnImmediately proves a zero or negative
// delay does not block, which keeps an accidental zero interval from
// stalling a sweep.
func TestWaitContextZeroAndNegativeReturnImmediately(t *testing.T) {
	t.Parallel()

	for _, delay := range []time.Duration{0, -time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			t.Parallel()

			start := time.Now()
			err := waitContext(context.Background(), delay)
			elapsed := time.Since(start)

			require.NoError(t, err)
			assert.Less(t, elapsed, waitLimit)
		})
	}
}
