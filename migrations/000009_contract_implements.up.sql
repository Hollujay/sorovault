-- Standard IDs detected from the current decoded contract interface.
-- Existing rows are classified on their next successful refresh.
ALTER TABLE contracts ADD COLUMN implements TEXT[] NOT NULL DEFAULT '{}';
