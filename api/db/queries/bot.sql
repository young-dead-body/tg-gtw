-- name: GetUserByTelegram :one
SELECT * FROM users
WHERE telegram ILIKE $1
LIMIT 1;

-- name: SetUserPassword :one
UPDATE users
SET password_hash = $2, password_changed_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListUsersWithExpiry :many
SELECT * FROM users
WHERE is_active = true AND expires_at IS NOT NULL
ORDER BY expires_at ASC;