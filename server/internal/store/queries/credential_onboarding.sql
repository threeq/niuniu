-- Generic one-time credential-onboarding tokens (issue #727). A single table
-- backs every credential-bearing onboarding flow (provider credentials, data
-- sources), generalizing the IM-Bot-specific im_bot_onboarding_tokens. Only the
-- sha256 hex of the raw token is stored; the raw token is returned once and
-- never persisted. All comments in this file must be pure ASCII.

-- name: CreateCredentialOnboardingToken :one
INSERT INTO credential_onboarding_tokens (token_hash, project_id, issued_by, kind, title, fields_json, target_json, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, token_hash, project_id, issued_by, kind, title, fields_json, target_json, expires_at, used_at, created_at;

-- name: GetCredentialOnboardingTokenByHash :one
SELECT id, token_hash, project_id, issued_by, kind, title, fields_json, target_json, expires_at, used_at, created_at
FROM credential_onboarding_tokens
WHERE token_hash = ?;

-- name: MarkCredentialOnboardingTokenUsed :exec
UPDATE credential_onboarding_tokens SET used_at = CURRENT_TIMESTAMP WHERE id = ?;

-- name: DeleteExpiredCredentialOnboardingTokens :exec
DELETE FROM credential_onboarding_tokens WHERE expires_at < CURRENT_TIMESTAMP;
