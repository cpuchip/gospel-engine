-- Michael, 2026-10-10: "Align to ibeco.me at 600 q/m". New keys default to 600 requests a minute, the limit ibeco.me
-- has always asked for when it mints a user's key. Existing rows keep the limit they were given.
ALTER TABLE api_tokens ALTER COLUMN rate_limit SET DEFAULT 600;

INSERT INTO schema_migrations (version) VALUES (7) ON CONFLICT DO NOTHING;
