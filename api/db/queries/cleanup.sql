-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions
WHERE expires_at < now();

-- name: DeleteOldRegistrationRequests :execrows
DELETE FROM registration_requests
WHERE status IN ('pending', 'rejected')
  AND created_at < now() - make_interval(secs => $1);