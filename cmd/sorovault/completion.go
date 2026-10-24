package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/config"
	"github.com/soroworks/sorovault/internal/stellar"
	"github.com/soroworks/sorovault/internal/store"
)

// Completion is best-effort by contract: a shell calls a completion function
// on every Tab, and anything written to stdout that is not a candidate is
// pasted into the user's command line, while a non-zero exit aborts the whole
// completion. Cobra's own guidance is that a completion function must never
// print and never fail, so everything here returns a directive instead of an
// error and keeps its diagnostics on stderr.
//
// The lookup is also bounded: the shell is waiting, so a slow or unreachable
// database must not hold the prompt open.
const completionTimeout = 750 * time.Millisecond

// maxCompletionItems caps how many candidates a completion offers. A registry
// can hold thousands of contracts, and past a screenful the list stops helping
// and starts costing the shell time to render.
const maxCompletionItems = 200

// registerContractIDCompletion attaches contract-ID completion to a command's
// positional argument, so the commands that take a contract ID all offer the
// same candidates from one implementation.
//
// A command whose contract IDs are optional and mutually exclusive with a flag
// passes the name of that flag; when it is set, completion offers nothing
// rather than suggesting IDs the command would then reject. refresh is the one
// such command: it takes IDs or --all, never both.
func registerContractIDCompletion(cmd *cobra.Command, exclusiveFlag ...string) {
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		for _, flag := range exclusiveFlag {
			if f := cmd.Flags().Lookup(flag); f != nil && f.Changed {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
		}
		// These commands take exactly one contract ID, so once it is given
		// there is nothing further to complete and offering more would be
		// misleading.
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return completeContractIDs(toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

// completeContractIDs lists the registry's contracts for completion, rendering
// a named contract as "ID\tname" so the candidate is readable rather than a
// wall of 56-character identifiers.
//
// Every failure path returns no candidates rather than an error, because a
// completion function that fails is worse for the user than one that is simply
// unhelpful: the database being down should not break the shell prompt.
func completeContractIDs(toComplete string) []string {
	return completeContractIDsFrom(queryContractIDs, toComplete)
}

// completeContractIDsFrom is completeContractIDs with the lookup injected, so
// the failure path can be tested without turning the process's environment into
// the test fixture.
func completeContractIDsFrom(lookup func(context.Context) ([]contractCandidate, error), toComplete string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	ids, err := lookup(ctx)
	if err != nil {
		// Deliberately swallowed: see the note above. The reason is logged
		// at debug so it can be found when someone is actually diagnosing
		// completion rather than doing it silently.
		debugf("contract completion unavailable: %v", err)
		return nil
	}

	return renderContractCandidates(ids, toComplete)
}

// renderContractCandidates turns stored contracts into shell candidates,
// filtering on the prefix the user has typed and capping the count.
//
// It is a pure function so the formatting and filtering rules can be tested
// without a database, which is where the interesting cases are: a named
// contract rendered as "ID\tname", a case-insensitive prefix, and the cap that
// keeps a large registry from flooding the prompt.
func renderContractCandidates(candidates []contractCandidate, toComplete string) []string {
	out := make([]string, 0, len(candidates))
	for _, c := range candidates {
		if !hasPrefixFold(c.ContractID, toComplete) {
			continue
		}
		if c.Name != "" {
			out = append(out, c.ContractID+"\t"+c.Name)
		} else {
			out = append(out, c.ContractID)
		}
		if len(out) >= maxCompletionItems {
			break
		}
	}
	return out
}

// contractCandidate is the part of a stored contract completion needs: the ID
// to offer and the name to describe it with.
type contractCandidate struct {
	ContractID string
	Name       string
}

// queryContractIDs opens the database, lists the contracts and closes it again.
//
// It uses withStore rather than withApp so completion never depends on RPC: an
// unreachable network must not be able to make contract completion fail, and
// the candidates come from the registry the user configured either way.
func queryContractIDs(ctx context.Context) ([]contractCandidate, error) {
	var candidates []contractCandidate

	err := withStore(ctx, func(ctx context.Context, db store.Store) error {
		var err error
		candidates, err = listContractCandidates(ctx, db)
		return err
	})
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// listContractCandidates reads the candidate contracts from an already-open
// store.
//
// It is separate from queryContractIDs so the query itself can be exercised
// against the in-memory store, which is the only way to test the successful
// path without a Postgres instance. Completion reads at most maxCompletionItems
// contracts, because the shell will never show more than that.
func listContractCandidates(ctx context.Context, db store.Store) ([]contractCandidate, error) {
	page, err := db.ListContracts(ctx, store.ListFilter{Limit: maxCompletionItems})
	if err != nil {
		return nil, err
	}

	candidates := make([]contractCandidate, 0, len(page.Contracts))
	for _, c := range page.Contracts {
		candidates = append(candidates, contractCandidate{ContractID: c.ContractID, Name: c.Name})
	}
	return candidates, nil
}

// registerNetworkCompletion attaches completion to a command's --network flag.
//
// Network names come from configuration and the well-known passphrases, so this
// needs no database and cannot fail: it is the one completion here that is
// always available.
func registerNetworkCompletion(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("network", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return completeNetworks(), cobra.ShellCompDirectiveNoFileComp
	})
}

// completeNetworks returns the network names a user can select.
//
// The three well-known networks are always offered, because --network accepts
// them whether or not they are in the registry yet. The network this instance
// is configured for is added too: a private or standalone network is named by
// its passphrase, so it is a valid --network value that the well-known three do
// not cover. Any further networks recorded in the registry are a best-effort
// bonus.
func completeNetworks() []string {
	seen := map[string]bool{}
	out := make([]string, 0, 5)

	add := func(name string) {
		// "unknown" is what an empty passphrase maps to; it is a
		// placeholder, not a name anyone selects.
		if name == "" || name == "unknown" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	add(stellar.NetworkName(stellar.NetworkPublic))
	add(stellar.NetworkName(stellar.NetworkTestnet))
	add(stellar.NetworkName(stellar.NetworkFuturenet))

	// The configured passphrase names this instance's own network, which for a
	// private chain is not one of the three above.
	if cfg, err := config.Load(); err == nil {
		add(stellar.NetworkName(cfg.NetworkPassphrase))
	} else {
		debugf("network completion cannot read configuration: %v", err)
	}

	// The registry is the authority on which names actually exist, but only as
	// a bonus: the names above already make the flag useful, so a database that
	// cannot be reached costs nothing.
	ctx, cancel := context.WithTimeout(context.Background(), completionTimeout)
	defer cancel()

	names, err := queryNetworks(ctx)
	if err != nil {
		debugf("network completion unavailable: %v", err)
		return out
	}
	for _, n := range names {
		add(n)
	}
	return out
}

// queryNetworks lists the networks recorded in the registry.
func queryNetworks(ctx context.Context) ([]string, error) {
	var names []string

	err := withStore(ctx, func(ctx context.Context, db store.Store) error {
		var err error
		names, err = db.ListNetworks(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

// hasPrefixFold reports whether s starts with prefix, ignoring case.
//
// Shell completion is case-insensitive in practice — a user typing "cdlz" means
// the contract whose ID begins "CDLZ" — so filtering is too.
func hasPrefixFold(s, prefix string) bool {
	if prefix == "" {
		return true
	}
	if len(prefix) > len(s) {
		return false
	}
	return equalFoldASCII(s[:len(prefix)], prefix)
}

// equalFoldASCII compares ASCII strings without regard to case.
//
// It is deliberately not strings.EqualFold: contract IDs and network names are
// ASCII, and the simple loop makes the intent obvious without pulling in
// Unicode case folding for inputs that never need it.
func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// debugf writes a completion diagnostic to stderr when debug logging is on.
//
// Completion runs before the command does, so it has no logger in hand. It
// reads LOG_LEVEL directly and says nothing unless that is debug; anything more
// elaborate would be machinery invented only to explain why a Tab press did not
// list something. Nothing here may ever reach stdout.
func debugf(format string, args ...any) {
	if !strings.EqualFold(os.Getenv("LOG_LEVEL"), "debug") {
		return
	}
	fmt.Fprintf(os.Stderr, "completion: "+format+"\n", args...)
}
