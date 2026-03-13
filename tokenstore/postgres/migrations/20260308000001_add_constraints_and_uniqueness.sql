-- migrate:up
ALTER TABLE objectstore_tokens
    ADD COLUMN max_size       BIGINT  NOT NULL DEFAULT 0,
    ADD COLUMN allowed_types  TEXT[]  NOT NULL DEFAULT '{}',
    ADD COLUMN signature      TEXT    NOT NULL DEFAULT '',
    ADD COLUMN scope          TEXT    NOT NULL DEFAULT '';

-- Partial unique index: only active (non-revoked) tokens with a signature
CREATE UNIQUE INDEX idx_objectstore_tokens_scope_signature
    ON objectstore_tokens (scope, signature)
    WHERE signature != '' AND revoked = FALSE;

-- migrate:down
DROP INDEX IF EXISTS idx_objectstore_tokens_scope_signature;
ALTER TABLE objectstore_tokens
    DROP COLUMN max_size,
    DROP COLUMN allowed_types,
    DROP COLUMN signature,
    DROP COLUMN scope;
