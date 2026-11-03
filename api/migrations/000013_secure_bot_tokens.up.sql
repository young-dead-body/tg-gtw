CREATE TABLE telegram_bindings (
    user_id UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    telegram_user_id BIGINT NOT NULL UNIQUE CHECK (telegram_user_id > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Only token digests are stored. One outstanding token per user/purpose.
CREATE TABLE one_time_tokens (
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose TEXT NOT NULL CHECK (purpose IN ('telegram_link', 'password_reset')),
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, purpose)
);
CREATE INDEX one_time_tokens_expiry ON one_time_tokens(expires_at);
