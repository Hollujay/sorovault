// Command sorovault registers Soroban contracts and serves their decoded
// interfaces over HTTP.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// version is stamped at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags --always)"
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra has already printed the error; exit non-zero without
		// repeating it.
		if !errors.Is(err, errPrinted) {
			fmt.Fprintln(os.Stderr, "sorovault:", err)
		}
		os.Exit(1)
	}
}

// errPrinted marks an error whose message has already reached the user.
var errPrinted = errors.New("error already reported")

// configPath is the YAML file the --config flag names (CONFIG_PATH is the
// fallback). Commands read configuration through config.LoadWith(configPath),
// so one persistent flag reaches every command without a parameter threaded
// through each signature. The variable is empty until the flag is parsed,
// which is exactly Load's behavior.
var configPath string

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "sorovault",
		Short: "A contract metadata and interface registry for Soroban",
		Long: `SoroVault fetches a deployed Soroban contract's WASM, decodes the
interface embedded in it, and stores the result in a searchable registry
that both people and tooling can read.

Configuration is read from the environment:

  RPC_URL                     Stellar RPC endpoint      (default testnet)
  NETWORK_PASSPHRASE          expected network          (default testnet)
  DATABASE_URL                Postgres connection URL   (required; or DATABASE_URL_FILE)
  MAX_CONCURRENT_REGISTRATIONS registrations at once     (default 4)
  HTTP_ADDR                   listen address for serve  (default :8080)
  LOG_LEVEL                   debug|info|warn|error     (default info)
  REFRESH_EVENTS              refresh on upgrade events (default false; needs REFRESH_INTERVAL)
  REFRESH_EVENTS_INTERVAL     how often to read events  (default 10s)
  SIGNING_KEY                 sign interface responses  (base64 Ed25519 seed; or SIGNING_KEY_FILE)
  SIGNING_RETIRED_KEYS        old public keys to publish (comma-separated base64)

A YAML file can supply the same settings as the base layer:

  sorovault --config sorovault.yaml serve

The environment overrides the file, setting by setting, and the file's
keys are these same env-var names, case-insensitively. Run
"sorovault config show" to print the effective configuration with
secrets redacted.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
	}

	// configPath is the file the --config flag names; CONFIG_PATH is the
	// fallback. Every subcommand's body reads configuration through the
	// same LoadWith(configPath) call, so the file — once the flag is
	// parsed — is effective for all of them without plumbing a parameter
	// through each command's signature.
	root.PersistentFlags().StringVar(&configPath, "config", "", "YAML config file to load as the base layer (environment overrides it; CONFIG_PATH is the fallback)")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// The flag wins, as a typed option does over an ambient one.
		if configPath == "" {
			configPath = strings.TrimSpace(os.Getenv("CONFIG_PATH"))
		}
		return nil
	}

	root.AddCommand(
		newAddCmd(),
		newRemoveCmd(),
		newListCmd(),
		newGetCmd(),
		newVersionsCmd(),
		newCodegenCmd(),
		newCheckClientCmd(),
		newRefreshCmd(),
		newExportCmd(),
		newImportCmd(),
		newServeCmd(),
		newMigrateCmd(),
		newDoctorCmd(doctorDepsFromEnv),
		newBackfillCmd(),
		newClaimCmd(),
		newKeygenCmd(),
		newConfigCmd(),
	)

	return root
}
