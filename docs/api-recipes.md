# API recipes with `curl`

Worked examples for the SoroVault HTTP API. They assume a local server started
with `sorovault serve` (the default is `http://localhost:8080`) and the zkvote
contract from testnet, which is also the checked-in decoding fixture. Responses
are abbreviated with `…` and are independent illustrative snapshots rather than
one continuous session; the routes, field names and status codes are the ones
in the code and in [`internal/api/openapi.json`](../internal/api/openapi.json).

```bash
BASE=http://localhost:8080
CONTRACT=CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN
OLD_HASH=c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781
# NEW_HASH stands in for the hash an upgrade would produce.
NEW_HASH=0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7
```

Pass `network=testnet` explicitly on reads. Without it, single-contract
lookups default to the network the server's RPC endpoint serves, while the
listing endpoints (`/api/contracts`, `/api/upgrades`, `/api/stats`) span every
network in the database.

## 1. Check that the server is alive

```console
$ curl -sS "$BASE/healthz"
{"status":"ok","version":"1.2.3"}
```

`/healthz` does not touch PostgreSQL or RPC. It answers whether this process is
serving, not whether its dependencies are healthy.

## 2. Register a contract

Registration fetches the contract's WASM, decodes `contractspecv0`, and stores
the interface. It is idempotent: the first call returns **201**, and a repeat
returns **200**.

```console
$ curl -sS -w '\nHTTP %{http_code}\n' -X POST "$BASE/api/contracts" \
    -H 'content-type: application/json' \
    -d "{\"contract_id\":\"$CONTRACT\"}"
{
  "contract": {
    "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
    "network": "testnet",
    "current_wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
    "first_seen": "2026-08-07T09:14:11Z",
    "last_refreshed": "2026-08-07T09:14:11Z"
  },
  "interface": {
    "abi_version": "1",
    "functions": [ … ],
    "types": { "structs": [ … ], "unions": [], "enums": [], "error_enums": [ … ] },
    "events": [ … ]
  },
  "created": true,
  "changed": true
}
HTTP 201
```

A second call returns the same contract with `"created": false` and
`"changed": false` and status **200**. If the contract has been upgraded since
it was stored, `changed` is `true` and `previous_wasm_hash` names the old
version.

`contract_id` must be a 56-character strkey contract address (`C…`). The body
must be a single JSON object and unknown fields are rejected.

## 3. Register several contracts

Batches are processed one contract at a time, up to 20 IDs. One failure does
not stop the rest, and each result carries the status code that contract would
have had on its own request.

```console
$ curl -sS -X POST "$BASE/api/contracts:batch" \
    -H 'content-type: application/json' \
    -d '{"contract_ids":[
      "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
      "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V"
    ]}'
{
  "results": [
    {
      "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
      "status": "created",
      "created": true,
      "changed": true,
      "contract": { … },
      "interface": { … }
    },
    {
      "contract_id": "CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V",
      "status": "error",
      "created": false,
      "changed": false,
      "error": "stellar: contract not found on network: CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V",
      "code": 404
    }
  ],
  "created": 1,
  "unchanged": 0,
  "changed": 0,
  "failed": 1
}
```

`status` is `created`, `unchanged`, `changed` or `error`. The request itself is
**200** even when individual contracts fail; read the per-contract `code`.

## 4. Search by name, function or type

`?q=` is a case-insensitive substring match against the contract ID, its name,
and the names of functions, types and events in its **current** interface. A
match on an interface symbol is reported in `matches`.

```console
$ curl -sS "$BASE/api/contracts?q=vote&network=testnet&limit=2"
{
  "contracts": [
    {
      "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
      "network": "testnet",
      "current_wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
      "first_seen": "2026-08-07T09:14:11Z",
      "last_refreshed": "2026-08-07T12:00:00Z",
      "matches": ["VoteParams", "Voted", "get_total_votes", "get_vote_params", "has_voted", "vote"]
    }
  ],
  "total": 1,
  "limit": 2,
  "offset": 0
}
```

Exact-name filters ask a different question: "which contracts implement this
interface?" Both `function` and `type` are repeatable, case-sensitive, and every
supplied name must match.

```console
$ curl -sS "$BASE/api/contracts?function=vote&function=has_voted&network=testnet"
```

```json
{
  "contracts": [ { "contract_id": "CDZZ…4PAN", "network": "testnet", … } ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

The same filter with `?type=Proposal` matches a user-defined struct, union, enum
or error enum named exactly `Proposal`. Filters combine with `?q=` and
`?network=`; all conditions must match.

## 5. Fetch a contract's decoded interface

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT?network=testnet"
{
  "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
  "network": "testnet",
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "first_seen": "2026-08-07T09:14:11Z",
  "decoded_at": "2026-08-07T09:14:11Z",
  "current": true,
  "interface": {
    "abi_version": "1",
    "meta": {
      "rssdkver": "25.3.0#dcbea44513feb7734af6b6c4aced2c4a7a2715d0",
      "rsver": "1.93.0-nightly"
    },
    "env_meta": { "interface_version": "25", "protocol": 25 },
    "functions": [
      {
        "name": "has_voted",
        "inputs": [
          {
            "name": "nullifier",
            "type": { "kind": "bytes_n", "display": "BytesN<32>", "n": 32 }
          }
        ],
        "outputs": [ { "kind": "bool", "display": "bool" } ]
      },
      …
    ],
    "types": { "structs": [ … ], "unions": [], "enums": [], "error_enums": [ … ] },
    "events": [ … ]
  }
}
```

The response is the public ABI JSON; `abi_version` is **1**. `current` is
`true` because no `wasm_hash` selected an older version. Empty collections are
`[]`, never `null`.

## 6. List versions and read a superseded interface

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT/versions?network=testnet"
{
  "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
  "network": "testnet",
  "versions": [
    {
      "wasm_hash": "0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7",
      "decoded_at": "2026-09-01T10:02:00Z",
      "current": true
    },
    {
      "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
      "decoded_at": "2026-08-07T09:14:11Z",
      "current": false
    }
  ]
}
```

Newest first. To read the old interface rather than the current one, pass its
hash:

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT?network=testnet&wasm_hash=$OLD_HASH"
{
  "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
  "network": "testnet",
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "first_seen": "2026-08-07T09:14:11Z",
  "decoded_at": "2026-08-07T09:14:11Z",
  "current": false,
  "interface": { … }
}
```

Superseded versions stay retrievable forever by hash; they are not included in
`?q=` search.

## 7. List a contract's function signatures

A lightweight alternative to downloading the whole ABI:

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT/functions?network=testnet"
{
  "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
  "network": "testnet",
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "functions": [
    {
      "name": "vote",
      "signature": "fn vote(voter: Address, proposal_index: u32, pub_signals: Vec<BytesN<32>>, a: BytesN<64>, b: BytesN<128>, c: BytesN<64>) -> Result<(), Error>"
    },
    {
      "name": "has_voted",
      "signature": "fn has_voted(nullifier: BytesN<32>) -> bool"
    },
    {
      "name": "initialize",
      "signature": "fn initialize(admin: Address, verifier_id: Address, vote_params: VoteParams, proposal_descriptions: Vec<String>) -> Result<(), Error>"
    },
    …
  ]
}
```

The order is the order the functions appear in the contract's spec. Pass
`?wasm_hash=` to list an older version's functions.

## 8. Fetch one function in detail

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT/functions/has_voted?network=testnet"
{
  "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
  "network": "testnet",
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "signature": "fn has_voted(nullifier: BytesN<32>) -> bool",
  "function": {
    "name": "has_voted",
    "inputs": [
      {
        "name": "nullifier",
        "type": { "kind": "bytes_n", "display": "BytesN<32>", "n": 32 }
      }
    ],
    "outputs": [ { "kind": "bool", "display": "bool" } ]
  }
}
```

Function lookup is case-sensitive, matching Soroban symbol semantics. An
unknown function is **404**, even when the contract exists.

## 9. Get a JSON Schema for a function's arguments

The schema is JSON Schema 2020-12 and describes the arguments **object**; it is
derived from the stored interface and needs no RPC. It is useful for rendering
and validating a form before encoding a contract call.

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT/functions/has_voted/schema.json?network=testnet"
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "urn:sorovault:contract:CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN:testnet:c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781:has_voted",
  "title": "has_voted arguments",
  "description": "Arguments of has_voted on contract CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN (testnet).",
  "type": "object",
  "properties": {
    "nullifier": { "type": "string", "pattern": "^[0-9a-fA-F]{64}$" }
  },
  "required": ["nullifier"],
  "additionalProperties": false
}
```

The response content type is `application/schema+json`. Integers wider than 53
bits are decimal strings, addresses and byte strings carry patterns, options
are nullable, and user-defined types appear under `$defs`.

## 10. Download a typed TypeScript client

The client is generated from the stored interface. It needs no network access
and can be produced for an older version with `?wasm_hash=`.

```console
$ curl -sS "$BASE/api/contracts/$CONTRACT/client.ts?network=testnet" \
    -o zkvote.ts
$ head -n 12 zkvote.ts
// Code generated by SoroVault from contract CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN on testnet. DO NOT EDIT.
// Interface version (WASM hash): c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781
// Built with soroban-sdk 25.3.0#dcbea44513feb7734af6b6c4aced2c4a7a2715d0
//
// Requires @stellar/stellar-sdk >= 14 and buffer.

import { Buffer } from "buffer";
import * as sdk from "@stellar/stellar-sdk/contract";

export const contractId = "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN";
export const networkName = "testnet";
export const wasmHash = "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781";
```

Further down are an `Errors` table, an interface for every struct/union/enum,
one typed method per contract function, and:

```ts
export interface Client {
  vote(args: { voter: string; proposal_index: sdk.u32; pub_signals: Array<Buffer>; a: Buffer; b: Buffer; c: Buffer }, options?: sdk.MethodOptions): Promise<sdk.AssembledTransaction<sdk.Result<void>>>;
  has_voted(args: { nullifier: Buffer }, options?: sdk.MethodOptions): Promise<sdk.AssembledTransaction<boolean>>;
  …
}

export function connect(
  options: Omit<sdk.ClientOptions, "contractId"> & { contractId?: string },
): Promise<sdk.Client & Client> { … }
```

Add `?download=1` to get `Content-Disposition: attachment`. The generated file
requires `@stellar/stellar-sdk` 14 or later and `buffer`.

## 11. Follow the upgrades feed

Every stored interface version after a contract's first is an upgrade. The feed
is newest first and excludes first registrations. It is populated whenever a
new version is stored — by a refresh, a scheduled sweep, or registering a
contract that has been upgraded since it was last seen.

```console
$ curl -sS "$BASE/api/upgrades?network=testnet&limit=5"
{
  "upgrades": [
    {
      "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
      "network": "testnet",
      "old_wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
      "new_wasm_hash": "0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7",
      "observed_at": "2026-09-01T10:02:00Z"
    }
  ],
  "total": 1,
  "limit": 5,
  "offset": 0
}
```

To check one contract immediately instead of waiting for a sweep:

```console
$ curl -sS -X POST "$BASE/api/contracts/$CONTRACT/refresh"
{
  "contract": { … },
  "interface": { … },
  "created": false,
  "changed": true,
  "previous_wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781"
}
```

An unchanged contract returns `"changed": false` with no
`previous_wasm_hash`.

## 12. Subscribe to the upgrade feed as Atom

The same events, with the same paging parameters, as an Atom 1.0 feed:

```console
$ curl -sS "$BASE/api/upgrades.atom?network=testnet&limit=5"
<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <id>urn:sorovault:upgrades</id>
  <title>SoroVault interface upgrades</title>
  <subtitle>Contracts whose interface changed, newest first</subtitle>
  <updated>2026-09-01T10:02:00Z</updated>
  <author><name>SoroVault</name></author>
  <link rel="self" href="/api/upgrades.atom?limit=5&amp;network=testnet" type="application/atom+xml"/>
  <entry>
    <id>urn:sorovault:upgrade:testnet:CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN:0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7</id>
    <title>CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN upgraded on testnet</title>
    <updated>2026-09-01T10:02:00Z</updated>
    <link rel="alternate" href="/api/contracts/CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN?network=testnet" type="application/json"/>
    <summary>Contract CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN on testnet moved from WASM hash c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781 to 0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7.</summary>
  </entry>
</feed>
```

`link` hrefs are paths, not absolute URLs: the instance does not know the
address it is served under. A page with more events carries a `rel="next"`
link. The content type is `application/atom+xml`.

## 13. Read registry statistics

```console
$ curl -sS "$BASE/api/stats"
{
  "networks": [
    { "network": "testnet", "contracts": 1, "versions": 2 }
  ],
  "contracts": 1,
  "versions": 2,
  "latest_contract": {
    "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
    "network": "testnet",
    "current_wasm_hash": "0f5bd7b2e93efd1c07d2f5b1f0f2f2a1c0d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7",
    "first_seen": "2026-08-07T09:14:11Z",
    "last_refreshed": "2026-09-01T10:02:00Z"
  },
  "last_refresh": {
    "checked": 1,
    "changed": 1,
    "failed": 0,
    "busy": 0,
    "missing": 0,
    "completed_at": "2026-09-01T10:05:00Z"
  }
}
```

`last_refresh` is omitted when `REFRESH_INTERVAL` is `0` or no sweep has
finished. `latest_contract` is omitted when the registry is empty. Counts cover
every network in the database.

## 14. Find every contract running the same WASM

A WASM hash identifies the code, not the contract instance. This is the query a
vulnerability in a shared module needs:

```console
$ curl -sS "$BASE/api/wasm/$OLD_HASH/contracts?network=testnet"
{
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "network": "testnet",
  "ever": false,
  "contracts": [
    {
      "contract_id": "CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN",
      "network": "testnet",
      "current_wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
      "first_seen": "2026-08-07T09:14:11Z",
      "last_refreshed": "2026-08-07T12:00:00Z",
      "current": true
    }
  ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

`ever=true` also lists contracts that ran the hash in the past and have since
been upgraded; each result's `current` says whether it still does. A hash can
also be catalogued without requiring a contract instance, for example a module
uploaded ahead of deployment:

```console
$ curl -sS -X POST "$BASE/api/wasm/$OLD_HASH"
{
  "network": "testnet",
  "wasm_hash": "c618dae264864ccf446a3c7db27da80c7c83e840131242cc2fe9cd32a2a20781",
  "interface": { … },
  "decoded_at": "2026-08-07T09:14:11Z",
  "created": true
}
```

`POST` returns **201** for a newly catalogued hash and **200** when it was
already stored.

## Errors, paging and limits

Errors returned by these handlers are JSON. A contract that is not in the
registry:

```console
$ curl -sS "$BASE/api/contracts/CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V?network=testnet"
{"error":"store: not found: contract CDZZZPYQALFRBQAZD5KCW2SZK4ZF455PJVD7H53AGHXTWAJO4BYVPP2V on testnet"}
```

| Status | Meaning |
|---|---|
| **400** | Malformed JSON, an unknown field, a missing `contract_id`, an invalid contract ID, or an invalid WASM hash. |
| **404** | No such contract, version, function or catalogued WASM. |
| **422** | The contract exists but its interface cannot be served or generated: a Stellar asset contract, a module without `contractspecv0`, one over the decode limits, or a spec the TypeScript generator cannot express. |
| **503** | The registry is at its registration limit, or `RPC_URL` serves a different network than `NETWORK_PASSPHRASE`. A busy response carries `Retry-After` in seconds. |
| **500** | An unexpected server fault. The body is deliberately the generic `{"error":"internal error"}`. |

Paging uses `limit` (default 50, maximum 200) and `offset`. Values that cannot
be parsed fall back to the defaults rather than failing the request. Request
bodies are limited to 64 KiB; a batch is limited to 20 contract IDs.

For the full schema and every parameter, fetch the live document:

```console
$ curl -sS "$BASE/api/openapi.json"
```
