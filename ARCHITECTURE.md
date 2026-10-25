# Architecture

SoroVault turns a deployed Soroban contract ID into a stored, versioned
interface. This is the one-page map: what each package owns, how a registration
and a refresh sweep move through them, and the invariants that keep the stored
interfaces trustworthy.

Read [CONTRIBUTING.md](CONTRIBUTING.md) for setup and conventions. The README's
[How it fits together](README.md#how-it-fits-together) is the short version;
this document is the contributor's version.

## The shape of the system

```
      internal/api     internal/web     cmd/sorovault
            \               |               /
             +-------> internal/registry <------ internal/refresher
                          /       |       \
                  fetch  /    decode|        \ save
                        v           v         v
             internal/stellar  internal/spec  internal/store
                                     |              |
                                     v              v
                               internal/model   PostgreSQL / memory
```

`internal/codegen` reads a stored `model.Interface` and produces a TypeScript
client, a JSON Schema, or Markdown; it never talks to RPC. `internal/refresher`
drives `registry.Refresh` on a schedule and is wired in by `serve`.

## What each package owns

| Package | Owns |
|---|---|
| `cmd/sorovault` | Cobra commands, flag parsing, and the wiring in `app.go` and `serve.go`. `serve` mounts the API, UI and `/healthz`, applies the 60-second request timeout and 15-second shutdown grace, and starts the refresher when `REFRESH_INTERVAL` is positive. |
| `internal/config` | Environment-only configuration, defaults and validation. The only required setting is `DATABASE_URL` (or `DATABASE_URL_FILE`). |
| `internal/model` | The ABI types and JSON shape. It depends on nothing else in the project; this is the public contract other tools consume. Empty collections are `[]`, optional fields are omitted, and `ABIVersion` is bumped for a breaking change. |
| `internal/wasm` | The small part of the WASM binary format needed to find a named custom section. It walks top-level section framing and skips bodies; it does not execute or validate WASM code. |
| `internal/spec` | Turns the `contractspecv0` custom section into `model.Interface`. It reads the bare stream of XDR `SCSpecEntry` values, decodes `contractmetav0` and `contractenvmetav0`, and enforces the decode limits. |
| `internal/stellar` | The `stellar.Fetcher` seam and its RPC implementation. It reads the contract instance ledger entry to get the WASM hash, then the contract code entry, and validates contract IDs and network passphrases. It only reads. |
| `internal/store` | The `store.Store` seam, the Postgres implementation, the in-memory implementation used by tests, snapshots, and the embedded migration runner. |
| `internal/registry` | Orchestration and policy: network resolution and passphrase guard, the shared concurrency budget, `Register`, `Refresh`, `CatalogWasm`, and the `Result` the API and CLI render. |
| `internal/refresher` | Scheduled sweeps, pacing, sweep summaries, and the best-effort upgrade webhook. |
| `internal/api` | chi handlers, request/response JSON, status-code mapping, compression, interface caching, the Atom feed and the embedded OpenAPI document. |
| `internal/web` | Server-rendered browse UI, markdown rendering and sanitisation, and the same-origin check on the registration form. |
| `internal/codegen` | TypeScript clients and packages, JSON Schema for a function's arguments, and Markdown reference docs. Pure transformations of a stored interface. |
| `internal/stellar/stellartest` | A fake network used by tests. Production code never imports it. |
| `embed.go`, `migrations/` | Migrations are embedded into the binary through `embed.go`; templates and static files are embedded by `internal/web`. The image needs no files beside the binary. |

## The three seams

Three interfaces are the places to extend without reaching through layers:

- **`stellar.Fetcher`** (`internal/stellar/stellar.go`) — where contract code
  and network identity come from.
- **`spec.Decoder`** (`internal/spec/spec.go`) — how bytes become an interface.
- **`store.Store`** (`internal/store/store.go`) — where the registry lives.

Nothing above a seam depends on the implementation below it. That is why the
API, registry and refresher tests run without a network or a database: they use
`stellartest`, a stub decoder, and the in-memory store.

`store.Store` has two implementations, `store.Postgres` and `store.Memory`.
Both run the conformance suite in `internal/store/store_test.go`; a change to
one that is not made to the other will fail that suite.

## The life of a registration

`POST /api/contracts`, the CLI's `add`, and the browse UI's form all end at
`registry.Register`:

1. **Validate.** `registry.Register` calls `stellar.ValidateContractID` before
   spending a concurrency slot or an RPC round trip. A malformed strkey is a
   client error, not a server fault.
2. **Take a slot.** `registry.acquire` takes one slot from a weighted
   semaphore shared by the API, the CLI and the refresher. It waits up to two
   seconds, then returns `registry.ErrBusy`; the API turns that into **503**
   with `Retry-After`. The slot is released on every path out.
3. **Resolve the network.** `registry.Network` asks the RPC endpoint for its
   passphrase, compares it with `NETWORK_PASSPHRASE`, and maps it to a short
   label (`public`, `testnet`, `futurenet`, or the passphrase itself). The label
   and any mismatch are cached for the life of the process; a transient RPC
   failure is not cached.
4. **Remember what was there.** `store.GetContract` is read before the fetch,
   because it is what distinguishes a new contract from an already-registered
   one. `ErrNotFound` is expected.
5. **Fetch.** `stellar.FetchContract` performs two `getLedgerEntries` calls:
   the contract instance names a WASM hash, and the contract code entry for
   that hash holds the module bytes.
6. **Decode.** `spec.XDRDecoder.Decode` finds `contractspecv0` with
   `wasm.CustomSection`, reads the XDR entries, converts them with
   `spec.FromEntries`, and attaches the optional metadata sections.
7. **Store.** `store.Save` upserts the `contracts` row and writes the `specs`
   row keyed by `(network, contract_id, wasm_hash)`. The same hash updates the
   existing row in place; a different hash inserts a new version and repoints
   `contracts.current_wasm_hash`.
8. **Report.** The saved contract is read back and returned as
   `registry.Result` with `Created` and `Changed`. The API maps `Created` to
   **201** and an already-known contract to **200**, which makes retries safe.

`registry.CatalogWasm` follows the same fetch/decode/store shape for uploaded
code that no contract references yet. It validates the 64-character hash,
verifies the fetched bytes against it, and stores the interface in
`wasm_specs` keyed by `(network, wasm_hash)`.

## The life of a refresh

There are two entry points, and they share `registry.Refresh`:

- `POST /api/contracts/{id}/refresh` refreshes one contract.
- `internal/refresher` refreshes stale contracts on a schedule. It is started
  by `serve` only when `REFRESH_INTERVAL` is positive.

`registry.Refresh` deliberately starts from what is stored:

1. Take a concurrency slot and resolve the network, as registration does.
2. Load the existing contract. Refreshing something never registered is an
   error, not an implicit registration.
3. Call `stellar.WasmHash`, which reads only the instance ledger entry. This
   is the cheap question: has the pointer changed?
4. **Unchanged:** load the stored spec, call `store.Save` with it to advance
   `last_refreshed` and clear any `missing_since` mark, and return with
   `Changed` false. No module is downloaded.
5. **Changed:** call `FetchContract`, decode the new module, and `Save` it. The
   new hash appends a `specs` row; the old row is untouched. The result carries
   `Changed` true and `PreviousWasmHash`.
6. **Gone:** if the instance entry is missing, `markMissing` records
   `missing_since` and returns `Missing` true. The stored interface is kept and
   still served; this is not an error and not a failed refresh.

A **sweep** is `refresher.Sweep`:

- `staleContracts` pages through every contract on the refresher's network
  whose `last_refreshed` is older than the interval. The full list is collected
  before any refresh runs, because refreshing moves a contract to the front of
  the listing and paging while writing would skip and repeat rows.
- The sweep paces refreshes evenly across the interval rather than firing them
  all at once. A sweep therefore normally takes about one interval; an RPC
  limit is a schedule, not just a concurrency limit.
- Each contract is refreshed under `RPC_TIMEOUT`. A failure is counted and
  logged and the sweep continues. A busy registry increments `Busy` and leaves
  the contract stale for the next sweep. A missing contract increments
  `Missing` and is logged once, when the mark is first set.
- A changed contract is passed to the optional webhook notifier. Delivery is
  best effort: one POST, five-second timeout, no retries.
- The completed `Summary` is kept in memory and exposed by `GET /api/stats` as
  `last_refresh`. It is not persisted; a restart begins a new history.

`refresher.SweepAll` is the CLI's `sorovault refresh --all`: every contract
once, regardless of staleness, without pacing. It is meant for a manual catch-up
or a cron job, not for `serve`.

## The data model

```
contracts (network, contract_id) PRIMARY KEY
  current_wasm_hash, name, source_url, source_commit,
  first_seen, last_refreshed, missing_since

specs (network, contract_id, wasm_hash) UNIQUE
  spec jsonb, symbols, decoded_at
  FOREIGN KEY (network, contract_id) -> contracts ON DELETE CASCADE

wasm_specs (network, wasm_hash) PRIMARY KEY
  spec jsonb, decoded_at
```

`specs.symbols` is the newline-joined, sorted set of function, type and event
names. Search matches against it instead of walking jsonb for every query; the
current spec is the one that is searched, so a function removed by an upgrade
stops matching. `wasm_specs` is separate because uploaded code can be
catalogued before any contract instance references it.

## Invariants to keep

These are the rules the rest of the design assumes. A change that breaks one is
not a local change.

1. **An interface is identified by its WASM hash.** A given
   `(network, contract_id, wasm_hash)` always describes the same module, so a
   stored interface is never replaced by one for a different hash. Upgrades
   append; they do not overwrite. Re-registering the same hash is idempotent
   and must not create a duplicate version.
2. **Nothing from the network is executed.** `internal/wasm` only walks section
   framing; `internal/spec` only decodes XDR. There is no WASM runtime and no
   VM. SoroVault never sends a transaction, never calls a contract, and only
   uses read-only RPC methods. Keep it that way: an interface is data, and
   uploaded code is untrusted input.
3. **Decode limits are part of the trust boundary.** `wasm.MaxModuleSize`
   (2 MiB), `spec.MaxSpecSectionSize` (1 MiB) and `spec.MaxSpecEntries`
   (10,000) bound the work a hostile module can cause. They map to **422**, not
   **500**, because they describe the input. Do not bypass them in a new
   decoder or a new fetch path.
4. **Every record is namespaced by network.** All store keys include the
   network, and writes use the network resolved from RPC. The passphrase check
   is a guard against filing mainnet contracts under `testnet`; do not weaken
   it to make a misconfigured endpoint "work".
5. **Registration and refresh concurrency is bounded in one place.** Every
   `Register` and `Refresh` call goes through `registry.acquire`, because peak
   memory is the number of modules in flight. Do not call `FetchContract` and
   `Decode` directly from a handler, a command, or the refresher. `CatalogWasm`
   is the deliberate exception: it is a one-off catalogue operation for an
   uploaded hash, not a bulk path.
6. **The ABI JSON is a public contract.** `internal/model` is consumed by
   other tools. Adding an optional field is compatible; renaming or removing a
   field, or changing a type, requires `model.ABIVersion` to be bumped and the
   README to be updated.
7. **Migrations are paired and append-only.** Every
   `migrations/NNNNNN_name.up.sql` has a matching `.down.sql`.
   `store.Migrate` applies forward; `store.MigrateDown` rolls the schema back
   and is used for local resets, not exposed by the CLI. Never edit a migration
   that has shipped. Add a new numbered pair, and run `make test-all` so the
   Postgres conformance suite sees it.
8. **Both stores behave the same.** New store behaviour belongs in the
   conformance suite first. The in-memory store is what most tests use to
   stand in for Postgres, so drift between them hides bugs rather than catching
   them.
9. **The API and UI are read-mostly and unauthenticated.** The only writes are
   registering/cataloguing, refreshing, and attaching source metadata. There is
   no authentication by design; deployment is expected to provide it. The UI's
   registration form additionally requires a same-origin `Origin` or `Referer`.

## Adding a route or changing the ABI

- Add the handler in `internal/api/api.go` and document it in
  `internal/api/openapi.json`. `TestOpenAPICoversEveryRoute` fails if a route
  and the document disagree.
- If the response shape changes, change `internal/model` and consider
  `ABIVersion`. Existing stored jsonb is read back through the same types.
- If the change affects decoding, extend `internal/spec` and regenerate the
  golden fixture with `make golden`; review the diff rather than blessing it.
- If it affects storage, add a migration pair and extend the conformance suite.
- `make check` runs the linter, `go vet` and the full test suite. `make test-all`
  adds the Postgres suite via `TEST_DATABASE_URL`.
