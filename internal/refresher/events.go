package refresher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

const (
	// defaultEventsInterval is how often FollowEvents polls when none is
	// configured; the network closes a ledger about every five seconds.
	defaultEventsInterval = 10 * time.Second

	// eventPageSize is how many events one request reads.
	eventPageSize = 200
	// maxPagesPerPoll bounds how long one poll can run when the follower has
	// fallen far behind; the next tick carries on from the saved cursor.
	maxPagesPerPoll = 50

	cursorPrefix = "cursor:"
	ledgerPrefix = "ledger:"
)

// stateKey is where a network's event position is kept. It is per network
// because one database may hold several, and a cursor from one means nothing
// on another.
func stateKey(network string) string { return "refresher.events." + network }

// FollowEvents polls for contract-upgrade events until ctx is cancelled,
// refreshing each upgraded contract as soon as it is seen.
//
// A failed poll is logged and retried on the next tick: like the sweep, the
// loop must outlive an RPC outage. The sweep keeps running beside it, so
// anything the events miss is still found, only later.
func (r *Refresher) FollowEvents(ctx context.Context) {
	ticker := time.NewTicker(r.eventsInterval)
	defer ticker.Stop()

	failing := false
	for {
		sum, err := r.PollEvents(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			// One line per outage, not one per tick.
			if !failing {
				r.log.WarnContext(ctx, "following upgrade events failed")
			}
			failing = true
		case err == nil:
			if failing {
				r.log.InfoContext(ctx, "following upgrade events recovered")
			}
			failing = false
			if sum.Checked > 0 {
				r.log.InfoContext(ctx, "refreshed upgraded contracts",
					"checked", sum.Checked, "changed", sum.Changed,
					"missing", sum.Missing, "failed", sum.Failed, "busy", sum.Busy)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollEvents reads the upgrade events published since the last poll and
// refreshes the registered contracts they name.
//
// The position read up to is saved after each page is handled, so a restart
// resumes where it left off and an event is handled at least once. Handling
// is idempotent: a contract that already runs the new code is left alone.
//
// Two situations leave the position unusable, and both are answered with a
// full sweep rather than a guess about what was missed:
//
//   - there is no saved position, because the follower has never run; and
//   - the saved position is older than the RPC's retention, so upgrades
//     between it and the oldest retained ledger can no longer be read.
//
// In both, the position is first moved to the newest ledger and only then
// is the sweep run, so an upgrade that lands during the sweep is read by the
// next poll instead of falling between the two.
func (r *Refresher) PollEvents(ctx context.Context) (Summary, error) {
	var sum Summary
	if r.events == nil {
		return sum, errors.New("refresher: no event source configured")
	}

	network, err := r.reg.Network(ctx)
	if err != nil {
		return sum, err
	}
	st := r.reg.Store()
	key := stateKey(network)

	saved, err := st.GetState(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		r.log.InfoContext(ctx, "no saved event position, starting from now after a full sweep",
			"network", stellar.NetworkName(network))
		return r.resync(ctx, network)
	}
	if err != nil {
		return sum, fmt.Errorf("refresher: reading event position: %w", err)
	}

	pos := parsePosition(saved)
	for range maxPagesPerPoll {
		page, err := r.events.ExecutableUpdates(ctx, pos, eventPageSize)
		if errors.Is(err, stellar.ErrPositionExpired) {
			r.log.WarnContext(ctx, "event position is older than the RPC retains, falling back to a full sweep",
				"network", stellar.NetworkName(network))
			resynced, err := r.resync(ctx, network)
			sum.add(resynced)
			return sum, err
		}
		if err != nil {
			return sum, err
		}

		pageSum := r.refreshUpdated(ctx, network, page.Updates)
		sum.add(pageSum)

		// A contract the registry was too busy to refresh keeps the page
		// unconsumed, so the next poll offers it again.
		if pageSum.Busy > 0 {
			return sum, nil
		}

		if page.Cursor != "" {
			if err := st.SetState(ctx, key, cursorPrefix+page.Cursor); err != nil {
				return sum, fmt.Errorf("refresher: saving event position: %w", err)
			}
			pos = stellar.UpdatePosition{Cursor: page.Cursor}
		}
		if !page.More {
			return sum, nil
		}
	}
	return sum, nil
}

// resync moves the saved position to the newest ledger and then sweeps every
// contract once.
func (r *Refresher) resync(ctx context.Context, network string) (Summary, error) {
	latest, err := r.events.LatestLedger(ctx)
	if err != nil {
		return Summary{}, err
	}
	if err := r.reg.Store().SetState(ctx, stateKey(network), ledgerPrefix+strconv.FormatUint(uint64(latest), 10)); err != nil {
		return Summary{}, fmt.Errorf("refresher: saving event position: %w", err)
	}
	return r.SweepAll(ctx)
}

// refreshUpdated refreshes the registered contracts a page of events names.
// A contract upgraded several times in a page is refreshed once, since a
// refresh goes straight to its current code.
func (r *Refresher) refreshUpdated(ctx context.Context, network string, updates []stellar.ExecutableUpdate) Summary {
	var sum Summary

	// The last event per contract decides what it now runs.
	latest := make(map[string]stellar.ExecutableUpdate, len(updates))
	var order []string
	for _, u := range updates {
		if _, seen := latest[u.ContractID]; !seen {
			order = append(order, u.ContractID)
		}
		latest[u.ContractID] = u
	}

	for _, id := range order {
		if ctx.Err() != nil {
			return sum
		}
		contract, err := r.reg.Store().GetContract(ctx, network, id)
		if err != nil {
			// Almost every contract on the network is not in this registry.
			if !errors.Is(err, store.ErrNotFound) {
				sum.Failed++
				r.log.WarnContext(ctx, "looking up upgraded contract failed",
					"network", stellar.NetworkName(network))
			}
			continue
		}
		if contract.CurrentWasmHash == latest[id].NewWasmHash && contract.MissingSince == nil {
			continue // already refreshed, by the sweep or an earlier poll
		}
		r.refreshContract(ctx, network, *contract, &sum)
	}
	return sum
}

// parsePosition reads a saved position. Anything unrecognised is treated as
// an expired cursor by the source, which resyncs.
func parsePosition(saved string) stellar.UpdatePosition {
	switch {
	case strings.HasPrefix(saved, ledgerPrefix):
		n, err := strconv.ParseUint(strings.TrimPrefix(saved, ledgerPrefix), 10, 32)
		if err != nil {
			return stellar.UpdatePosition{Cursor: saved}
		}
		return stellar.UpdatePosition{StartLedger: uint32(n)}
	case strings.HasPrefix(saved, cursorPrefix):
		return stellar.UpdatePosition{Cursor: strings.TrimPrefix(saved, cursorPrefix)}
	default:
		return stellar.UpdatePosition{Cursor: saved}
	}
}

// add accumulates another summary into s.
func (s *Summary) add(o Summary) {
	s.Checked += o.Checked
	s.Changed += o.Changed
	s.Failed += o.Failed
	s.Busy += o.Busy
	s.Missing += o.Missing
}
