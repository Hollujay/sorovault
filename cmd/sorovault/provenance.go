package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/soroworks/sorovault/internal/attest"
	"github.com/soroworks/sorovault/internal/store"
)

func newBackfillCmd() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "backfill-origins",
		Short: "Find the creator and creation ledger of contracts that lack them",
		Long: `Look up who created each registered contract that has no recorded creator,
and in which ledger, and store the answer.

The RPC only keeps a recent window of ledgers, so a contract created before
that window cannot be traced and stays marked unknown. Running the command
again later is harmless; it only looks at contracts still lacking an origin.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return withApp(cmd.Context(), func(ctx context.Context, a *app) error {
				sum, err := a.registry.BackfillOrigins(ctx)
				if err != nil {
					return err
				}
				if asJSON {
					return writeJSON(cmd.OutOrStdout(), sum)
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "checked %d, found %d, unknown %d, failed %d\n",
					sum.Checked, sum.Found, sum.Unknown, sum.Failed)
				if sum.Failed > 0 {
					return fmt.Errorf("%w: %d contract(s) failed", errPrinted, sum.Failed)
				}
				return nil
			})
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the summary as JSON")
	return cmd
}

// secretEnv is where `claim sign` reads the deploying account's secret seed.
// It is an environment variable, never a flag, so the seed stays out of
// shell history and the process list.
const secretEnv = "SOROVAULT_PUBLISHER_SECRET"

func newClaimCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "claim",
		Short: "Prepare a signed publisher claim",
	}
	cmd.AddCommand(newClaimSignCmd())
	return cmd
}

func newClaimSignCmd() *cobra.Command {
	var (
		network      string
		name         string
		sourceURL    string
		sourceCommit string
		wasmHash     string
		issuedAt     string
	)

	cmd := &cobra.Command{
		Use:   "sign <contract_id>",
		Short: "Sign a claim with the deploying account's key",
		Long: `Build and sign a publisher claim for a contract, printing the JSON body to
POST to /api/contracts/<id>/verify.

The secret seed of the account that deployed the contract is read from the
` + secretEnv + ` environment variable. Signing happens locally; the seed
is never sent anywhere. The claim is only accepted for fifteen minutes, so
sign it just before submitting.

  export ` + secretEnv + `=S...
  sorovault claim sign C... --name "My Vault" --source-url https://github.com/me/vault \
    | curl -X POST -H 'content-type: application/json' -d @- https://registry/api/contracts/C.../verify`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			secret := strings.TrimSpace(os.Getenv(secretEnv))
			if secret == "" {
				return fmt.Errorf("set %s to the deploying account's secret seed", secretEnv)
			}

			if network == "" {
				// The network is part of what is signed, so it must be
				// chosen deliberately. Asking the configured RPC is the
				// same answer the registry itself will give.
				err := withApp(cmd.Context(), func(ctx context.Context, a *app) error {
					var err error
					network, err = a.registry.Network(ctx)
					return err
				})
				if err != nil {
					return fmt.Errorf("resolving network (pass --network to avoid asking the RPC): %w", err)
				}
			}

			if issuedAt == "" {
				issuedAt = time.Now().UTC().Format(time.RFC3339)
			}
			claim := attest.Claim{
				Network:      network,
				ContractID:   args[0],
				Name:         name,
				SourceURL:    sourceURL,
				SourceCommit: sourceCommit,
				WasmHash:     wasmHash,
				IssuedAt:     issuedAt,
			}
			signer, signature, err := attest.SignClaim(secret, claim)
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), map[string]any{
				"claim":     claim,
				"signer":    signer,
				"signature": signature,
			})
		},
	}

	cmd.Flags().StringVar(&network, "network", "", "network the contract is on (default: the network RPC_URL serves)")
	cmd.Flags().StringVar(&name, "name", "", "the name to publish the contract under (required)")
	cmd.Flags().StringVar(&sourceURL, "source-url", "", "repository the contract was built from (required)")
	cmd.Flags().StringVar(&sourceCommit, "source-commit", "", "commit the contract was built from")
	cmd.Flags().StringVar(&wasmHash, "wasm-hash", "", "pin the claim to this version of the contract")
	cmd.Flags().StringVar(&issuedAt, "issued-at", "", "RFC 3339 time to sign as (default: now)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("source-url")
	return cmd
}

func newKeygenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keygen",
		Short: "Generate a signing key for interface attestations",
		Long: `Generate a random Ed25519 signing key.

The secret seed is printed on standard output, to be set as SIGNING_KEY (or
written to a file named by SIGNING_KEY_FILE). The public key and its id go
to standard error; they are what clients verify against, and what
GET /api/keys publishes once the registry runs with the key.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			seed, err := attest.GenerateSeed(rand.Reader)
			if err != nil {
				return err
			}
			signer, err := attest.NewSigner(seed)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), base64.StdEncoding.EncodeToString(seed))
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "key id      %s\npublic key  %s\n",
				signer.KeyID(), base64.StdEncoding.EncodeToString(signer.PublicKey()))
			return nil
		},
	}
}

// printOrigin writes a contract's provenance and verification lines.
func printOrigin(w io.Writer, c store.Contract) {
	if c.Creator == "" {
		_, _ = fmt.Fprintln(w, "  creator    unknown")
	} else {
		_, _ = fmt.Fprintf(w, "  creator    %s (ledger %d)\n", c.Creator, c.CreatedLedger)
	}
	if c.Verified() {
		_, _ = fmt.Fprintf(w, "  verified   by %s on %s\n", c.VerifiedBy, c.VerifiedAt.UTC().Format("2006-01-02"))
	}
}
