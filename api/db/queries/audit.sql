-- name: InsertAuditLog :one
INSERT INTO audit_log (user_id, action, entity_type, entity_id, ip, user_agent, details)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, user_id, action, entity_type, entity_id, ip, user_agent, details, created_at;

-- name: ListAuditLogs :many
SELECT id, user_id, action, entity_type, entity_id, ip, user_agent, details, created_at
FROM audit_log
WHERE ($1::uuid IS NULL OR user_id = $1)
  AND ($2::text = '' OR action = $2)
  AND ($3::text = '' OR entity_type = $3)
  AND ($4::timestamptz IS NULL OR created_at >= $4)
  AND ($5::timestamptz IS NULL OR created_at <= $5)
ORDER BY created_at DESC
LIMIT $6 OFFSET $7;
