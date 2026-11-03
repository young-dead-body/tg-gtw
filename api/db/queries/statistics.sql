-- name: InsertStatistics :one
INSERT INTO statistics (
    user_id,
    ip,
    country,
    region,
    city,
    language,
    user_agent,
    browser,
    os,
    device,
    screen_resolution,
    login_at,
    auth_error
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: UpdateStatisticsDuration :exec
UPDATE statistics
SET session_duration_seconds = sub.duration
FROM (
    SELECT id, EXTRACT(EPOCH FROM (now() - login_at))::int AS duration
    FROM statistics
    WHERE statistics.user_id = $1
      AND session_duration_seconds IS NULL
      AND auth_error IS NULL
    ORDER BY login_at DESC
    LIMIT 1
) AS sub
WHERE statistics.id = sub.id;
-- name: GetStatistics :many
SELECT * FROM statistics
WHERE ($1::uuid IS NULL OR user_id = $1)
ORDER BY login_at DESC
LIMIT $2 OFFSET $3;

-- name: CountSuccessfulLoginsSince :one
SELECT count(*) FROM statistics WHERE auth_error IS NULL AND login_at >= $1;

-- name: CountLoginsTotal :one
SELECT count(*) FROM statistics;
