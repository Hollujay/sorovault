-- Small key/value state that must survive a restart, such as the cursor the
-- refresher has read contract-upgrade events up to.
CREATE TABLE state (
    key        TEXT        PRIMARY KEY,
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
