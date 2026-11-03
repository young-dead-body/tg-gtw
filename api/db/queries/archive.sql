-- name: ArchiveOldAuditLogs :exec
WITH moving AS (
    INSERT INTO audit_log_archive (id, user_id, action, entity_type, entity_id, ip, user_agent, details, created_at)
    SELECT audit_log.id, audit_log.user_id, audit_log.action, audit_log.entity_type,
           audit_log.entity_id, audit_log.ip, audit_log.user_agent, audit_log.details,
           audit_log.created_at
    FROM audit_log
    WHERE audit_log.created_at < $1
    RETURNING id
)
DELETE FROM audit_log AS log
WHERE log.created_at < $1;

-- name: CountOldAuditLogs :one
SELECT count(*) FROM audit_log WHERE created_at < $1;

-- name: DeleteArchivedLogs :exec
DELETE FROM audit_log_archive;