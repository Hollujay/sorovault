package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// LocateOrigin determines who created a contract and in which ledger, and
// records it. found is false, with a nil error, when the reachable history
// does not say; that is the normal outcome for an old contract.
//
// It needs a fetcher that can find origins. With one that cannot, found is
// false and so is any error: provenance is optional, and a registry that
// cannot establish it must still register contracts.
func (r *Registry) LocateOrigin(ctx context.Context, contractID string) (found bool, err error) {
	if err := stellar.ValidateContractID(contractID); err != nil {
		return false, err
	}
	network, err := r.Network(ctx)
	if err != nil {
		return false, err
	}
	finder, ok := r.fetcher.(stellar.OriginFinder)
	if !ok {
		return false, nil
	}

	origin, err := finder.FindOrigin(ctx, contractID)
	switch {
	case errors.Is(err, stellar.ErrOriginUnknown):
		return false, nil
	case err != nil:
		return false, err
	}
	if err := r.store.SetOrigin(ctx, network, contractID, origin.Creator, origin.Ledger); err != nil {
		return false, err
	}
	return true, nil
}

// recordOrigin is LocateOrigin for a registration that has already
// succeeded. Failing to find an origin must not fail the registration it
// accompanies, so errors are logged and dropped, unless the caller gave up.
func (r *Registry) recordOrigin(ctx context.Context, contractID string) {
	found, err := r.LocateOrigin(ctx, contractID)
	switch {
	case err != nil && ctx.Err() == nil:
		r.log.WarnContext(ctx, "could not determine contract origin")
	case !found && err == nil:
		r.log.DebugContext(ctx, "contract origin unknown")
	}
}

// BackfillSummary reports what BackfillOrigins did.
type BackfillSummary struct {
	// Checked is how many contracts lacked an origin.
	Checked int `json:"checked"`
	// Found is how many of them now have one.
	Found int `json:"found"`
	// Unknown is how many could not be determined.
	Unknown int `json:"unknown"`
	// Failed is how many hit an error other than not being found.
	Failed int `json:"failed"`
}

// BackfillOrigins fills in the origin of every contract on the registry's
// network that has none. A failure on one contract is counted and logged and
// does not stop the rest.
func (r *Registry) BackfillOrigins(ctx context.Context) (BackfillSummary, error) {
	var sum BackfillSummary

	network, err := r.Network(ctx)
	if err != nil {
		return sum, err
	}

	// The set is collected before any contract is updated, so paging is not
	// disturbed by the updates it causes.
	var todo []string
	filter := store.ListFilter{Network: network, Limit: store.MaxLimit}
	for {
		page, err := r.store.ListContracts(ctx, filter)
		if err != nil {
			return sum, fmt.Errorf("registry: listing contracts: %w", err)
		}
		for _, c := range page.Contracts {
			if c.Creator == "" {
				todo = append(todo, c.ContractID)
			}
		}
		filter.Offset += len(page.Contracts)
		if len(page.Contracts) == 0 || filter.Offset >= page.Total {
			break
		}
	}

	for _, id := range todo {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		sum.Checked++
		found, err := r.LocateOrigin(ctx, id)
		switch {
		case err != nil:
			sum.Failed++
			r.log.WarnContext(ctx, "backfill failed")
		case found:
			sum.Found++
		default:
			sum.Unknown++
		}
	}
	return sum, nil
}
