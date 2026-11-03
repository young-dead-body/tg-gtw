-- name: CreateRegistrationRequest :one
INSERT INTO registration_requests (
    first_name, last_name, middle_name, email, telegram,
    department, position, reason
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: ListRegistrationRequests :many
SELECT * FROM registration_requests
ORDER BY created_at DESC;

-- name: GetRegistrationRequest :one
SELECT * FROM registration_requests WHERE id = $1;

-- name: UpdateRegistrationRequestStatus :one
UPDATE registration_requests
SET status = $2, reviewed_at = now(), reviewed_by = $3
WHERE id = $1
RETURNING *;
-- name: CountPendingRegistrationRequests :one
SELECT count(*) FROM registration_requests WHERE status = 'pending';
