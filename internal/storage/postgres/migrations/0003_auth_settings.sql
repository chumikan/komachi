-- Preserve existing case-sensitive user identity and opaque credential formats.
CREATE TABLE users (
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL UNIQUE,
 password TEXT NOT NULL,
 email TEXT NOT NULL UNIQUE,
 role TEXT NOT NULL,
 created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
 totp_secret_encrypted TEXT NOT NULL DEFAULT '',
 totp_enabled INTEGER NOT NULL DEFAULT 0,
 totp_recovery_codes_json TEXT NOT NULL DEFAULT '[]',
 totp_enabled_at TEXT,
 totp_last_reset_at TEXT,
 must_set_password INTEGER NOT NULL DEFAULT 0
);
-- Sessions hold JWT JTIs, never the serialized JWT or a plaintext secret.
CREATE TABLE sessions (
 id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token_type TEXT NOT NULL,
 created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, revoked_at BIGINT
);
CREATE INDEX sessions_user_type ON sessions(user_id,token_type);
CREATE INDEX sessions_expiration ON sessions(expires_at);
CREATE TABLE api_keys (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, user_id TEXT NOT NULL,
 prefix TEXT NOT NULL UNIQUE, key_hash TEXT NOT NULL, role TEXT NOT NULL,
 expires_at BIGINT, created_by TEXT NOT NULL, created_at BIGINT NOT NULL,
 last_used_at BIGINT, revoked_at BIGINT
);
CREATE INDEX api_keys_user ON api_keys(user_id);
CREATE TABLE email_tokens (
 id TEXT PRIMARY KEY, token_hash TEXT NOT NULL, user_id TEXT NOT NULL,
 purpose TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL,
 consumed_at BIGINT
);
CREATE INDEX email_tokens_user ON email_tokens(user_id);
CREATE INDEX email_tokens_expiration ON email_tokens(expires_at);
-- Existing stores allow orphan references and the system actor. Do not reject
-- legacy rows or change deletion behavior by imposing new actor/page FKs.
CREATE TABLE favorites (
 user_id TEXT NOT NULL, page_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL,
 PRIMARY KEY(user_id,page_id)
);
CREATE INDEX favorites_user_order ON favorites(user_id,created_at DESC);
CREATE INDEX favorites_page ON favorites(page_id);
CREATE TABLE user_settings (
 user_id TEXT PRIMARY KEY, language TEXT NOT NULL, autosave BOOLEAN NOT NULL,
 date_format TEXT NOT NULL DEFAULT 'locale', time_format TEXT NOT NULL DEFAULT 'locale',
 updated_at TEXT NOT NULL -- existing RFC3339Nano API timestamp, without precision loss
);
-- Only the two existing runtime-managed JSON settings are represented.
CREATE TABLE instance_settings (
 key TEXT PRIMARY KEY CHECK(key IN ('branding','public_access')),
 value JSONB NOT NULL
);
