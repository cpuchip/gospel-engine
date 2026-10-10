-- v3: sign in with Google on engine.ibeco.me, so people can create, see and revoke their own API keys.
-- Follows ibeco.me's sign-in (same Google client, one-time state, 30-day server-side sessions) with a narrower scope
-- (openid email: no name, no picture). Keys minted here are ordinary api_tokens owned as external_user 'google:<sub>'.
CREATE TABLE IF NOT EXISTS users (
    id          BIGSERIAL PRIMARY KEY,
    google_sub  TEXT NOT NULL UNIQUE,
    email       TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- The cookie carries a random token; only its sha256 is stored. csrf guards the key page's forms.
CREATE TABLE IF NOT EXISTS sessions (
    token_hash  TEXT PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at  TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

INSERT INTO schema_migrations (version) VALUES (6) ON CONFLICT DO NOTHING;
