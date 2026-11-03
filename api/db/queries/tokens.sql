-- name: IssueOneTimeToken :exec
INSERT INTO one_time_tokens (user_id, purpose, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, purpose) DO UPDATE
SET token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at;

-- name: BindTelegram :one
WITH receipt AS (
    DELETE FROM one_time_tokens
    WHERE token_hash = $1 AND purpose = 'telegram_link' AND expires_at > now()
    RETURNING user_id
)
INSERT INTO telegram_bindings (user_id, telegram_user_id)
SELECT receipt.user_id, sqlc.arg('telegram_user_id') FROM receipt
JOIN users ON users.id = receipt.user_id
WHERE users.is_active AND users.blocked_at IS NULL
  AND (users.expires_at IS NULL OR users.expires_at > now())
ON CONFLICT (user_id) DO UPDATE SET telegram_user_id = EXCLUDED.telegram_user_id, created_at = now()
RETURNING *;

-- name: GetUserByTelegramID :one
SELECT users.* FROM users JOIN telegram_bindings ON telegram_bindings.user_id = users.id
WHERE telegram_bindings.telegram_user_id = $1;

-- name: GetTelegramBinding :one
SELECT * FROM telegram_bindings WHERE user_id = $1;

-- name: GetPasswordResetUser :one
SELECT users.* FROM users JOIN one_time_tokens ON one_time_tokens.user_id = users.id
WHERE token_hash = $1 AND purpose = 'password_reset' AND one_time_tokens.expires_at > now()
  AND users.is_active AND users.blocked_at IS NULL
  AND (users.expires_at IS NULL OR users.expires_at > now());

-- name: RedeemPasswordReset :one
WITH receipt AS (
    DELETE FROM one_time_tokens
    WHERE token_hash = $1 AND purpose = 'password_reset' AND expires_at > now()
    RETURNING user_id
)
UPDATE users SET password_hash = sqlc.arg('password_hash'), password_changed_at = now(), updated_at = now()
FROM receipt WHERE users.id = receipt.user_id
  AND users.is_active AND users.blocked_at IS NULL
  AND (users.expires_at IS NULL OR users.expires_at > now())
RETURNING users.*;

-- name: DeleteExpiredTokens :exec
DELETE FROM one_time_tokens WHERE expires_at <= now();

-- name: IssuePasswordResetToken :one
WITH binding AS (
    SELECT b.user_id FROM telegram_bindings b JOIN users u ON u.id = b.user_id
    WHERE b.telegram_user_id = sqlc.arg('telegram_user_id')
      AND u.is_active AND u.blocked_at IS NULL
      AND (u.expires_at IS NULL OR u.expires_at > now())
    FOR SHARE OF b
)
INSERT INTO one_time_tokens (user_id, purpose, token_hash, expires_at)
SELECT binding.user_id, 'password_reset', sqlc.arg('token_hash'), sqlc.arg('expires_at') FROM binding
ON CONFLICT (user_id, purpose) DO UPDATE
SET token_hash = EXCLUDED.token_hash, expires_at = EXCLUDED.expires_at
RETURNING *;
