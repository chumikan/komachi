-- SQLite timestamp values may contain nanoseconds. Preserve their original
-- representation during import; timestamptz alone has only microsecond precision.
ALTER TABLE users ADD COLUMN created_at_legacy TEXT;
ALTER TABLE user_settings ADD COLUMN updated_at_legacy TEXT;
ALTER TABLE favorites ADD COLUMN created_at_legacy TEXT;
ALTER TABLE favorites ADD COLUMN created_at_submicro SMALLINT NOT NULL DEFAULT 0
    CHECK (created_at_submicro BETWEEN 0 AND 999);
DROP INDEX favorites_user_order;
CREATE INDEX favorites_user_order ON favorites(user_id, created_at DESC, created_at_submicro DESC);
