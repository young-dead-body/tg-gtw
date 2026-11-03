-- name: InsertPasswordHistory :one
INSERT INTO password_history (user_id, password_hash)
VALUES ($1, $2)
RETURNING *;

-- name: ListRecentPasswordHashes :many
SELECT password_hash
FROM password_history
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2;