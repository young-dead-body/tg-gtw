-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC;

-- name: CreateUser :one
INSERT INTO users (
    first_name, last_name, middle_name, email, telegram,
    department, position, role, password_hash, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: UpdateUserPassword :one
UPDATE users
SET password_hash = $2, password_changed_at = now(), updated_at = now()
WHERE id = $1
RETURNING *;

-- name: TouchPasswordChangedAt :exec
UPDATE users
SET password_changed_at = now(), updated_at = now()
WHERE id = $1;

-- name: UpdateUserProfile :one
UPDATE users
SET first_name = $2, last_name = $3, middle_name = $4, telegram = $5,
    department = $6, position = $7, role = $8, is_active = $9,
    blocked_at = $10, expires_at = $11, password_hash = $12, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteUser :one
DELETE FROM users WHERE id = $1
RETURNING *;
-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: CountBlockedUsers :one
SELECT count(*) FROM users WHERE blocked_at IS NOT NULL;
