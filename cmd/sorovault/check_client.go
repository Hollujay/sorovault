package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

// apiBase is where check-client asks about the contract when --api is not
// given, matching serve's default listen address.
const apiBase = "http://localhost:8080"

// generatedConst matches a constant line the generator always writes:
//
//	export const wasmHash = "<hash>";
var generatedConst = regexp.MustCompile(`(?m)^export const (\w+) = "([^"]*)";$`)

// methodSig matches one method's signature line inside the generated
// Client interface, quoting the name only when the contract's function
// name is not a bare TypeScript identifier:
//
//	vote(args: { nullifier: Buffer }, options?: sdk.MethodOptions): …;
var methodSig = regexp.MustCompile(`^ {2}(?:"([^"]+)"|([A-Za-z_$][A-Za-z0-9_$]*))\(`)

// wasmHashLine isolates the embedded hash in a generated client.
var wasmHashLine = regexp.MustCompile(`(?m)^export const wasmHash = "[^"]*";$`)

func newCheckClientCmd() *cobra.Command {
	var (
		contract string
		network  string
		apiURL   string
	)

	cmd := &cobra.Command{
		Use:   "check-client <client.ts> --contract <contract_id>",
		Short: "Check a committed generated client against the current interface",
		Long: `Compare a committed generated TypeScript client with what SoroVault
would generate now, so a CI job can catch a client that was forgotten
when the contract was upgraded.

The command reads the WASM hash the client embeds — generated clients
carry it as the wasmHash constant next to contractId — asks SoroVault
for the contract's live hash and its current client, and reports what
differs, with the functions added, removed or changed rather than just
the bytes. It exits non-zero when anything differs.

The check only reads: it needs no database and no RPC, just access to
SoroVault's API.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckClient(cmd, args[0], contract, network, apiURL)
		},
	}
	cmd.Flags().StringVar(&contract, "contract", "", "contract ID the client was generated for (required)")
	cmd.Flags().StringVar(&network, "network", "", "network to check against (default: the server's own)")
	cmd.Flags().StringVar(&apiURL, "api", envAPIBase(), "SoroVault API base URL")
	_ = cmd.MarkFlagRequired("contract")
	return cmd
}

// envAPIBase takes the server address from the environment, so a CI job
// can pin the registry it checks against without editing the command
// line.
func envAPIBase() string {
	if v := strings.TrimSpace(os.Getenv("SOROVAULT_API")); v != "" {
		return v
	}
	return apiBase
}

// runCheckClient compares the committed client at path against
// SoroVault's record. Every finding is printed even where the command
// will exit clean, because a CI log is the consumer.
func runCheckClient(cmd *cobra.Command, path, contract, network, apiURL string) error {
	out := cmd.ErrOrStderr()
	api := strings.TrimRight(apiURL, "/")

	committed, err := os.ReadFile(path) //nolint:gosec // the path is what the user pointed at
	if err != nil {
		return fmt.Errorf("reading the client: %w", err)
	}

	embedded := parseEmbeddedSource(string(committed))
	if embedded.contractID == "" && embedded.wasmHash == "" {
		return fmt.Errorf("%s is not a SoroVault-generated client: no contractId or wasmHash constant to read", path)
	}
	if embedded.contractID != "" && embedded.contractID != contract {
		_, _ = fmt.Fprintf(out, "the client points at contract %s, but --contract says %s\n", embedded.contractID, contract)
		return fmt.Errorf("%w: the client was generated for a different contract", errPrinted)
	}
	if embedded.wasmHash == "" {
		_, _ = fmt.Fprintf(out, "%s carries no wasmHash constant; regenerate the client and commit the result\n", path)
		return fmt.Errorf("%w: the client predates embedded hashes", errPrinted)
	}

	liveHash, err := currentWasmHash(cmd.Context(), api, contract, network)
	if err != nil {
		return err
	}

	stale := false
	if embedded.wasmHash != liveHash {
		_, _ = fmt.Fprintf(out, "the contract upgraded: the client was generated for %s, the live hash is %s\n",
			embedded.wasmHash, liveHash)
		stale = true
	}

	current, err := fetchGeneratedClient(cmd.Context(), api, contract, network)
	if err != nil {
		return err
	}

	// The hash pin necessarily differs for an old client, so the content
	// comparison sets the pin aside: the embedded hash answers "same
	// version?" and the rest answers "same calls?".
	if stripWasmHash(string(committed)) != stripWasmHash(current) {
		added, removed, changed := diffClientMethods(string(committed), current)
		for _, name := range added {
			_, _ = fmt.Fprintf(out, "%s: added to the interface\n", name)
		}
		for _, name := range removed {
			_, _ = fmt.Fprintf(out, "%s: removed from the interface\n", name)
		}
		for _, name := range changed {
			_, _ = fmt.Fprintf(out, "%s: signature changed\n", name)
		}
		if len(added)+len(removed)+len(changed) == 0 {
			_, _ = fmt.Fprint(out, "no function signature changed, but the generated bytes differ\n")
		}
		stale = true
	}

	if stale {
		_, _ = fmt.Fprintf(out, "%s is stale; regenerate it and commit the result\n", path)
		return fmt.Errorf("%w: the committed client is out of date", errPrinted)
	}

	_, _ = fmt.Fprintf(out, "%s is up to date with %s\n", path, liveHash)
	return nil
}

// embeddedSource holds the constants a generated client states about
// itself.
type embeddedSource struct {
	contractID string
	wasmHash   string
}

// parseEmbeddedSource reads the contract ID and WASM hash a generated
// client declares, tolerating files that have one but not the other.
func parseEmbeddedSource(src string) embeddedSource {
	var e embeddedSource
	for _, m := range generatedConst.FindAllStringSubmatch(src, -1) {
		switch m[1] {
		case "contractId":
			e.contractID = m[2]
		case "wasmHash":
			e.wasmHash = m[2]
		}
	}
	return e
}

// stripWasmHash removes the embedded hash from a generated client.
func stripWasmHash(src string) string {
	return wasmHashLine.ReplaceAllString(src, "// wasm hash pin")
}

// currentWasmHash asks SoroVault which interface version the contract
// runs right now.
func currentWasmHash(ctx context.Context, api, contract, network string) (string, error) {
	target := api + "/api/contracts/" + url.PathEscape(contract) + query(network)

	var meta struct {
		WasmHash string `json:"wasm_hash"`
	}
	if err := fetchJSON(ctx, target, &meta); err != nil {
		return "", err
	}
	if meta.WasmHash == "" {
		return "", fmt.Errorf("%s answered without a wasm_hash", target)
	}
	return meta.WasmHash, nil
}

// fetchGeneratedClient downloads the client SoroVault would generate now.
func fetchGeneratedClient(ctx context.Context, api, contract, network string) (string, error) {
	target := api + "/api/contracts/" + url.PathEscape(contract) + "/client.ts" + query(network)

	body, status, err := httpRead(ctx, target)
	if err != nil {
		return "", err
	}
	if status == http.StatusNotFound {
		return "", fmt.Errorf("SoroVault has no TypeScript client for %s (%s); is the contract registered there?", contract, target)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s answered %d: %s", target, status, truncate(string(body), 200))
	}
	return string(body), nil
}

// httpRead performs a GET and returns the (capped) body and status. Both
// targets are caller-supplied check endpoints, and both are reads.
func httpRead(ctx context.Context, target string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil) //nolint:gosec // a caller-supplied check target, and a read on top of it
	if err != nil {
		return nil, 0, fmt.Errorf("building the request: %w", err)
	}
	answer, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("asking SoroVault: %w", err)
	}
	defer func() { _ = answer.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(answer.Body, 4<<20))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the answer: %w", err)
	}
	return body, answer.StatusCode, nil
}

// query appends the network selection, or nothing when the caller wants
// the server's default.
func query(network string) string {
	if network == "" {
		return ""
	}
	return "?network=" + url.QueryEscape(network)
}

// fetchJSON gets one JSON document and decodes it.
func fetchJSON(ctx context.Context, target string, v any) error {
	body, status, err := httpRead(ctx, target)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("%s answered %d: %s", target, status, truncate(string(body), 200))
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decoding %s: %w", target, err)
	}
	return nil
}

// diffClientMethods parses the Client interface out of both files and
// reports the functions that appear, disappear, or change shape, in
// declaration order.
func diffClientMethods(committed, current string) (added, removed, changed []string) {
	oldNames, oldMethods := declaredMethods(committed)
	newNames, newMethods := declaredMethods(current)

	for _, name := range oldNames {
		if _, ok := newMethods[name]; !ok {
			removed = append(removed, name)
			continue
		}
		if newMethods[name] != oldMethods[name] {
			changed = append(changed, name)
		}
	}
	for _, name := range newNames {
		if _, ok := oldMethods[name]; !ok {
			added = append(added, name)
		}
	}
	return added, removed, changed
}

// declaredMethods parses the generated Client interface block, returning
// the method names in declaration order and each one's signature line.
func declaredMethods(src string) ([]string, map[string]string) {
	open := strings.Index(src, "export interface Client {")
	if open < 0 {
		return nil, map[string]string{}
	}
	body := src[open+len("export interface Client {"):]

	var names []string
	methods := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		m := methodSig.FindStringSubmatch(strings.TrimRight(line, " \t\r"))
		if m == nil {
			continue
		}
		name := m[1]
		if m[2] != "" {
			name = m[2]
		}
		if _, seen := methods[name]; !seen {
			names = append(names, name)
			methods[name] = strings.TrimSpace(line)
		}
	}
	return names, methods
}
