# Contract `CDZZXVGUCLBUTE3WDD6TF2NXPTBPOYTVPPBBIZMSPVIQLUG226XJ4PAN`

Network: `testnet`  
WASM hash: `abc123`
  
Built with soroban-sdk `25.3.0#dcbea44513feb7734af6b6c4aced2c4a7a2715d0`

## Functions

### `fn vote(voter: Address, proposal_index: u32, pub_signals: Vec<BytesN<32>>, a: BytesN<64>, b: BytesN<128>, c: BytesN<64>) -> Result<(), Error>`

Cast a vote using a ZK Firma Digital Groth16 proof.

The proof is verified by a cross-contract call to the `zk_verifier`
contract.  Double-voting is prevented via the nullifier stored in
persistent storage.

### Public signals layout (5 × 32-byte big-endian scalars)


| idx | meaning                                                       |
|-----|---------------------------------------------------------------|
| 0   | `pubkeyHash`     — Poseidon hash of the signing RSA pubkey    |
| 1   | `nullifier`      — unique per voter; prevents double-voting   |
| 2   | `ageAbove18`     — reveal flag (0 or 1); not checked here     |
| 3   | `nullifierSeed`  — must equal `vote_params.vote_scope`        |
| 4   | `signalHash`     — binding commitment (see identity_gate)     |

* `voter`          – account casting the vote (must authorise the tx)
* `proposal_index` – 0-based index into the proposals list
* `pub_signals`    – 5 public circuit outputs (see table above)
* `a`              – Groth16 proof poi

- `voter: Address`
- `proposal_index: u32`
- `pub_signals: Vec<BytesN<32>>`
- `a: BytesN<64>`
- `b: BytesN<128>`
- `c: BytesN<64>`

Returns: `Result<(), Error>`

### `fn has_voted(nullifier: BytesN<32>) -> bool`

- `nullifier: BytesN<32>`

Returns: `bool`

### `fn initialize(admin: Address, verifier_id: Address, vote_params: VoteParams, proposal_descriptions: Vec<String>) -> Result<(), Error>`

One-time initialization.  Must be called before any votes can be cast.

* `admin`                 – address that deployed this contract
* `verifier_id`           – address of the deployed `zk_verifier` contract
* `vote_params`           – campaign configuration
* `proposal_descriptions` – human-readable label for each option

- `admin: Address`
- `verifier_id: Address`
- `vote_params: VoteParams`
- `proposal_descriptions: Vec<String>`

Returns: `Result<(), Error>`

### `fn get_proposal(index: u32) -> Result<Proposal, Error>`

- `index: u32`

Returns: `Result<Proposal, Error>`

### `fn get_total_votes() -> u64`

Returns: `u64`

### `fn get_vote_params() -> Result<VoteParams, Error>`

Returns: `Result<VoteParams, Error>`

### `fn get_proposal_count() -> u32`

Returns: `u32`

## Types

### `struct Proposal`

- `description: String`
- `vote_count: u64`

### `struct VoteParams`

Parameters defining a voting campaign.  Stored in contract storage and used as
inputs to the ZK circuit via public signals.  See `vote` method and circuit
comments for details on how these are used.

`vote_scope` acts as a domain separator so the same ZK identity cannot
be reused across different voting campaigns.

`citizenship_whitelist` stores encoded country codes (empty = unrestricted).
It is metadata only in the current circuit — enforcement would require a
dedicated circuit output signal.

- `birth_date_lowerbound: u64`
- `citizenship_whitelist: Vec<u64>`
- `expiration_date_lower_bound: u64`
- `identity_counter_upper_bound: u64`
- `identity_ts_upper_bound: u64`
- `vote_scope: u64`
- `voting_question: String`

### `enum DataKey`

Key under which a proposal is stored.

- `Admin` - The contract administrator.
- `Balance`(Address, i128) - A voter's balance.

### `enum Status`

Lifecycle of a proposal.

- `Open = 0` - Accepting votes.
- `Closed = 1` - No longer accepting votes.

### `error Error`

- `AlreadyInitialized = 1`
- `NotInitialized = 2`
- `InvalidProposalIndex = 3`
- `WrongNullifierSeed = 4`
- `AlreadyVoted = 5`
- `InvalidProof = 6`
- `WrongPubSignalsLen = 7`

## Events

### `Voted`

Topics: `voted`

- `voter: Address` (topic-list)
- `proposal_index: u32` (data)

Data format: `map`
