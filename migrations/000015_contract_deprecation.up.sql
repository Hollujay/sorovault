-- A migrated-away contract should not look alive to a user who finds it
-- in the registry: deprecation marks it, records the contract that
-- replaces it, and carries the note saying how to move over. The columns
-- live beside the source metadata, which the same PUT-style endpoints
-- already manage, and a refresh never touches them: being live on the
-- network says nothing about being the recommended contract to use.
ALTER TABLE contracts ADD COLUMN deprecated_since timestamptz;
ALTER TABLE contracts ADD COLUMN successor text NOT NULL DEFAULT '';
ALTER TABLE contracts ADD COLUMN deprecation_note text NOT NULL DEFAULT '';
