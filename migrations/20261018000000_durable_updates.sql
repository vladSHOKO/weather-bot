-- +goose Up
ALTER TABLE user_locations ADD CONSTRAINT latitude_range CHECK (latitude BETWEEN -90 AND 90);
ALTER TABLE user_locations ADD CONSTRAINT longitude_range CHECK (longitude BETWEEN -180 AND 180);
ALTER TABLE user_locations ADD CONSTRAINT timezone_nonempty CHECK (length(timezone) > 0);
CREATE TABLE bot_state (
 id integer PRIMARY KEY CHECK (id = 1),
 bot_id bigint NOT NULL,
 next_offset bigint NOT NULL DEFAULT 0 CHECK (next_offset >= 0)
);
CREATE TABLE bot_updates (
 update_id bigint PRIMARY KEY,
 chat_id bigint NOT NULL,
 payload jsonb NOT NULL,
 status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','done','failed')),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 available_at timestamptz NOT NULL DEFAULT now(),
 created_at timestamptz NOT NULL DEFAULT now(),
 finished_at timestamptz,
 failure_code text NOT NULL DEFAULT ''
);
CREATE INDEX bot_updates_pending ON bot_updates (status, available_at, update_id);
CREATE INDEX bot_updates_chat_order ON bot_updates (chat_id, update_id) WHERE status IN ('pending','running');

-- Expose only the applied marker for this schema, not mutable migration history.
CREATE VIEW bot_schema_version AS
SELECT version_id, is_applied FROM goose_db_version
WHERE version_id=20261018000000
ORDER BY id DESC LIMIT 1;

-- +goose Down
DROP VIEW bot_schema_version;
DROP TABLE bot_updates;
DROP TABLE bot_state;
ALTER TABLE user_locations DROP CONSTRAINT timezone_nonempty;
ALTER TABLE user_locations DROP CONSTRAINT longitude_range;
ALTER TABLE user_locations DROP CONSTRAINT latitude_range;
