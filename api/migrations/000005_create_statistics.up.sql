CREATE TABLE statistics (
    id                       BIGSERIAL PRIMARY KEY,
    user_id                  UUID REFERENCES users(id) ON DELETE SET NULL,
    ip                       INET,
    country                  TEXT,
    region                   TEXT,
    city                     TEXT,
    language                 TEXT,
    user_agent               TEXT,
    browser                  TEXT,
    os                       TEXT,
    device                   TEXT,
    screen_resolution        TEXT,
    login_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    session_duration_seconds INT,
    auth_error               TEXT
);

CREATE INDEX idx_statistics_login_at ON statistics(login_at);
CREATE INDEX idx_statistics_user_id ON statistics(user_id);