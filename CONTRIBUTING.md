# Contributing to SoroVault

Thanks for taking a look. SoroVault is a small, deliberately unfinished
codebase — the core is solid and the interesting work is mostly above it. This
document is about how to get moving quickly.

## Getting set up

You need Go **1.25** (the Stellar SDK requires it) and Docker for the database.
Alternatively, use the included dev container which provides Go, Node, golangci-lint,
and a Postgres service with TEST_DATABASE_URL set.

```console
$ git clone https://github.com/soroworks/sorovault && cd sorovault
$ make test
```

`make test` needs neither a database nor a network. If it passes, you are set
up correctly.

To run the whole thing:

```console
$ make up          # Postgres + migrations + server on :8080
```

Or against your own Postgres:

```console
$ cp .env.example .env && set -a && source .env && set +a
$ go run ./cmd/sorovault migrate
$ go run ./cmd/sorovault serve
```

## Before you open a pull request

```console
$ make lint        # go vet + gofmt check
$ make test-all    # tests, including the Postgres store suite
```

CI runs both, plus a Docker build and a `go mod tidy` check.

## Where things live

Read [the layout section of the README](README.md#how-it-fits-together) first.
The short version: three interfaces are the seams, and most changes belong on
one side of one of them. For the package map, the two write paths and the
invariants behind them, see [ARCHITECTURE.md](ARCHITECTURE.md).

| You want to… | Look at |
|---|---|
| change what the API returns | `internal/api`, and `internal/model` if the shape changes |
| support a new spec construct | `internal/spec` |
| store the registry somewhere else | implement `store.Store` |
| read contracts from somewhere else | implement `stellar.Fetcher` |
| change the browse UI | `internal/web` |

## Good first issues

These are real gaps, roughly easiest first:

1. **Populate `contracts.name`.** The column and the UI already handle it;
   nothing writes it. Deciding *where* a name comes from is most of the work.
2. **A background refresh poller.** Loop over registered contracts calling
   `registry.Refresh`, on an interval from the environment.
3. **Search across function and type names.** Today search is a substring match
   on contract ID and name. Reaching into the stored jsonb would make the
   registry far more useful.
4. **Codegen from the ABI.** The big one — see
   [open ground](README.md#open-ground). Typed clients generated from
   `GET /api/contracts/{id}`. This is a substantial project and deserves an
   issue to discuss the approach before code.

Please open an issue before starting on anything large, so nobody duplicates
work.

## Conventions

Nothing exotic — write Go that looks like the Go already here.

- **Comments explain why, not what.** The code says what it does. A comment
  earns its place by recording a constraint, a trade-off, or something
  surprising about the domain. Most functions need none.
- **Errors get context.** Wrap with `%w` and say what was being attempted:
  `fmt.Errorf("store: loading contract %s: %w", id, err)`. Sentinel errors
  (`store.ErrNotFound`, `stellar.ErrNoWasm`) are how callers make decisions —
  add one when a caller would otherwise have to match on a string.
- **Keep packages narrow.** `internal/model` depends on nothing; keep it that
  way, since it is what external consumers of the ABI mirror.
- **Log with `slog`**, and pass the logger in rather than reaching for a global.

### GitHub Actions

An action is pinned to a full commit SHA, with the version in a trailing
comment:

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
```

A tag is a mutable name, so pinning to one lets whoever can move the tag
decide what CI runs — and the release job holds a token that can write to
this repository. Dependabot still updates pinned actions: it rewrites the
SHA and the comment together.

Each workflow and job also declares the permissions it needs, and no more:
`contents: read` is enough for almost every job here, and a job that
really writes something says so.

## Testing

The rules that matter:

- **`go test ./...` must never need the network or a database.** This is not
  negotiable; it is what keeps the project pleasant to contribute to. Use the
  checked-in WASM fixture and the fakes in `internal/stellar/stellartest`.
- **Table-driven tests** where there are several cases of the same shape.
- **Test behaviour, not implementation.** A test that breaks on a refactor that
  changed no behaviour is a test that cost more than it earned.
- **Say why in the assertion message** when an assertion is not self-evident:
  `assert.Len(t, versions, 1, "re-registering an unchanged contract must be idempotent")`.

If you add a store implementation, run it through the existing conformance
suite in `internal/store/store_test.go` rather than writing a parallel one —
that is what keeps implementations honest about each other.

### The WASM fixture

`internal/spec/testdata/zkvote.wasm` is a real contract from testnet. Its
provenance is recorded in
[`internal/spec/testdata/README.md`](internal/spec/testdata/README.md), and a
test checks its SHA-256 against the on-chain hash it is named for.

If you intentionally change the ABI output shape, regenerate the golden file:

```console
$ make golden
$ git diff internal/spec/testdata/zkvote.golden.json   # review this carefully
```

`TestDecodeFixtureShape` asserts the important properties independently of the
golden file, so a careless regeneration cannot quietly bless a regression.

### Adding a fixture

If you need a contract the current one does not cover, fetch a real one rather
than hand-rolling a module — real contracts have the quirks that matter. Keep
it small, and add it to the testdata README with its contract ID, hash and the
date you fetched it.

## Changing the ABI JSON

`internal/model` is the project's public contract; other tools parse it. Adding
an optional field is fine. Renaming or removing one, or changing a type, is a
breaking change and needs `model.ABIVersion` bumped and a note in the README.

If you find yourself wanting to break it, open an issue first.

## Verifying against Stellar

Contract spec handling changes. If you are touching the fetch or decode path,
**check the current behaviour rather than trusting this repository** — the
README records what was verified and when, and that table should be updated by
whoever next confirms it.

Useful primary sources:

- [SEP-48, contract interface specification](https://github.com/stellar/stellar-protocol/blob/master/ecosystem/sep-0048.md)
- [`stellar/go-stellar-sdk`](https://github.com/stellar/go-stellar-sdk) — the `xdr` and `clients/rpcclient` packages
- [Stellar RPC `getLedgerEntries`](https://developers.stellar.org/docs/data/apis/rpc/api-reference/methods/getLedgerEntries)

## Licence

By contributing you agree that your contributions are licensed under
Apache-2.0, the same as the project.
