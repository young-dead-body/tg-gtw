CREATE TABLE proxy_pool (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    url            TEXT NOT NULL UNIQUE,
    status         TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'maintenance')),
    priority       INT NOT NULL DEFAULT 0,
    last_checked_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_proxy_pool_status ON proxy_pool(status);