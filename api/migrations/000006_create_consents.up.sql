CREATE TABLE consents (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID REFERENCES users(id) ON DELETE CASCADE,
    document_version TEXT NOT NULL,
    accepted_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    ip               INET,
    user_agent       TEXT
);

CREATE INDEX idx_consents_user_id ON consents(user_id);