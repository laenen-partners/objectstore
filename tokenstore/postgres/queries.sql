-- name: InsertToken :exec
INSERT INTO objectstore_tokens (token, method, bucket, key, expires_at, one_time, tags, max_size, allowed_types, signature, scope)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ValidateToken :one
SELECT id, token, method, bucket, key, expires_at, one_time, used, revoked, max_size, allowed_types
FROM objectstore_tokens
WHERE token = $1;

-- name: MarkUsed :exec
UPDATE objectstore_tokens
SET used = TRUE, used_at = NOW()
WHERE token = $1;

-- name: RevokeToken :exec
UPDATE objectstore_tokens
SET revoked = TRUE, revoked_at = NOW()
WHERE token = $1;

-- name: RevokeByTags :execrows
UPDATE objectstore_tokens
SET revoked = TRUE, revoked_at = NOW()
WHERE revoked = FALSE
  AND tags @> @tags::jsonb;

-- name: FindByTags :many
SELECT id, token, method, bucket, key, expires_at, one_time, used, revoked, tags, created_at
FROM objectstore_tokens
WHERE tags @> @tags::jsonb
ORDER BY created_at DESC;

-- name: CheckSignatureExists :one
SELECT EXISTS(
    SELECT 1 FROM objectstore_tokens
    WHERE scope = $1 AND signature = $2 AND signature != '' AND revoked = FALSE
) AS exists;
