-- migrate:up
CREATE TABLE IF NOT EXISTS objectstore_tokens (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token       TEXT        NOT NULL UNIQUE,
    method      TEXT        NOT NULL,
    bucket      TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    one_time    BOOLEAN     NOT NULL DEFAULT FALSE,
    used        BOOLEAN     NOT NULL DEFAULT FALSE,
    revoked     BOOLEAN     NOT NULL DEFAULT FALSE,
    tags        JSONB       NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_at     TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_objectstore_tokens_token ON objectstore_tokens (token);
CREATE INDEX IF NOT EXISTS idx_objectstore_tokens_expires_at ON objectstore_tokens (expires_at);
CREATE INDEX IF NOT EXISTS idx_objectstore_tokens_tags ON objectstore_tokens USING GIN (tags);

-- migrate:down
DROP TABLE IF EXISTS objectstore_tokens;
