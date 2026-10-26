-- Full-text search over a contract's doc comments. The column stores the
-- tsvector of every function and user-defined type doc joined with
-- newlines — the same docs, in the same order and with the same
-- separator, the Go joinDocs computes at Save time — so a query matches
-- the same words the memory store matches against its copy of the blob.
--
-- The 'simple' configuration means tokens are compared as lowercased
-- words with prefix matching, no stemming: a query "swap" finds "swaps"
-- because the prefix does, and identifiers in doc comments are matched
-- literally, the way a search over code documentation wants.
ALTER TABLE specs ADD COLUMN docs_tsv TSVECTOR;

-- Backfill rows written before this column existed. Function docs and
-- the doc of each of the four user-defined type groups, in the order
-- joinDocs walks them.
UPDATE specs
SET docs_tsv = to_tsvector('simple', COALESCE((
    SELECT string_agg(doc, E'\n')
    FROM (
        SELECT COALESCE(f->>'doc', '') AS doc
        FROM jsonb_array_elements(COALESCE(spec->'functions', '[]'::jsonb)) AS f
        UNION ALL
        SELECT COALESCE(t->>'doc', '')
        FROM jsonb_array_elements(COALESCE(spec->'types'->'structs', '[]'::jsonb)) AS t
        UNION ALL
        SELECT COALESCE(t->>'doc', '')
        FROM jsonb_array_elements(COALESCE(spec->'types'->'unions', '[]'::jsonb)) AS t
        UNION ALL
        SELECT COALESCE(t->>'doc', '')
        FROM jsonb_array_elements(COALESCE(spec->'types'->'enums', '[]'::jsonb)) AS t
        UNION ALL
        SELECT COALESCE(t->>'doc', '')
        FROM jsonb_array_elements(COALESCE(spec->'types'->'error_enums', '[]'::jsonb)) AS t
    ) AS docs), ''));

-- The index that makes a doc search a lookup instead of a scan.
CREATE INDEX specs_docs_tsv_idx ON specs USING GIN (docs_tsv);
