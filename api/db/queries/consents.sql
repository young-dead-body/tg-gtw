-- name: InsertConsent :one
INSERT INTO consents (user_id, document_version, ip, user_agent)
VALUES (sqlc.arg('user_id'), sqlc.arg('document_version'), sqlc.arg('ip'), sqlc.arg('user_agent'))
RETURNING *;