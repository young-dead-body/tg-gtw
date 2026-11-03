CREATE TABLE audit_log_archive (
    id          BIGINT PRIMARY KEY,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    action      TEXT NOT NULL,
    entity_type TEXT,
    entity_id   TEXT,
    ip          INET,
    user_agent  TEXT,
    details     JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_archive_created_at ON audit_log_archive(created_at);
CREATE INDEX idx_audit_log_archive_action ON audit_log_archive(action);