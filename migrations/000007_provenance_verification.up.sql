-- Provenance: the account that created the contract and the ledger it was
-- created in. Both are empty/zero when they could not be determined, which
-- is common for contracts older than the RPC's retention window.
--
-- Verification: set when a publisher proves control of the deploying
-- account by signing a claim. verified_wasm_hash is the version the claim
-- was made for, so a later upgrade is visible next to the badge.
ALTER TABLE contracts
    ADD COLUMN creator            TEXT        NOT NULL DEFAULT '',
    ADD COLUMN created_ledger     INTEGER     NOT NULL DEFAULT 0,
    ADD COLUMN verified_by        TEXT        NOT NULL DEFAULT '',
    ADD COLUMN verified_at        TIMESTAMPTZ,
    ADD COLUMN verified_wasm_hash TEXT        NOT NULL DEFAULT '';
