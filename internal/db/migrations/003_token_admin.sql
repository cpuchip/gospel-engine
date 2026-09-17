-- Admin tokens. Only a token with is_admin may call /api/admin/* (mint, list and
-- revoke tokens; reindex). Tokens minted through the API are never admin; admin
-- tokens come only from `gospel-engine bootstrap-token --admin` run inside the
-- container, so shell access to the container is the root of trust.
ALTER TABLE api_tokens ADD COLUMN IF NOT EXISTS is_admin BOOLEAN NOT NULL DEFAULT FALSE;

-- One-time promotion of the existing ibeco.me service token, which already
-- mints, lists and revokes user tokens on behalf of ibeco.me users. Promoting it
-- in the same transaction that introduces the role means ibeco.me never loses
-- access. The row is pinned by id AND name AND owner: before this migration any
-- valid token could mint a token with any name, so matching on name alone could
-- promote a look-alike. On any other deployment this statement matches nothing.
UPDATE api_tokens
   SET is_admin = TRUE
 WHERE id = 2
   AND name = 'ibeco.me-service'
   AND external_user = 'system:ibeco.me'
   AND revoked = FALSE;
