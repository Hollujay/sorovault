ALTER TABLE contracts
    DROP COLUMN creator,
    DROP COLUMN created_ledger,
    DROP COLUMN verified_by,
    DROP COLUMN verified_at,
    DROP COLUMN verified_wasm_hash;
