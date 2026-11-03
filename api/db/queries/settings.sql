-- name: GetSetting :one
SELECT * FROM settings WHERE key = $1;

-- name: SetSetting :exec
INSERT INTO settings (key, value, updated_at)
VALUES ($1, $2, now())
ON CONFLICT (key)
DO UPDATE SET value = EXCLUDED.value, updated_at = now();