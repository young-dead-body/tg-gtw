-- name: ListActiveProxies :many
SELECT * FROM proxy_pool
WHERE status = 'active'
ORDER BY priority ASC, created_at ASC;

-- name: ListProxies :many
SELECT * FROM proxy_pool
ORDER BY priority ASC, created_at ASC;

-- name: InsertProxyPool :one
INSERT INTO proxy_pool (url, status, priority)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdateProxyHealth :exec
UPDATE proxy_pool
SET status = $2, last_checked_at = now()
WHERE id = $1;

-- name: HasUserConsent :one
SELECT EXISTS(
    SELECT 1 FROM consents
    WHERE user_id = $1 AND document_version = COALESCE(
        (SELECT NULLIF(value #>> '{}', '') FROM settings WHERE key = 'consent.document_version'), '1.0'
    )
) AS ok;

-- name: LinkConsentToUser :exec
UPDATE consents
SET user_id = $1
WHERE user_id IS NULL AND id = sqlc.arg('consent_id')
  AND accepted_at > now() - interval '24 hours'
  AND document_version = COALESCE(
      (SELECT NULLIF(value #>> '{}', '') FROM settings WHERE key = 'consent.document_version'), '1.0'
  );