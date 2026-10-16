-- +goose Up
CREATE TABLE player_snapshots (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 snapshot_json TEXT NOT NULL,
 log_cursor INTEGER NOT NULL DEFAULT 0,
 history_floor INTEGER NOT NULL DEFAULT 0,
 event_floor INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE player_sessions (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 identity_key TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 generation TEXT NOT NULL,
 first_seen TEXT NOT NULL,
 last_seen TEXT NOT NULL,
 ended_at TEXT,
 gap INTEGER NOT NULL DEFAULT 0,
 end_reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX player_sessions_history ON player_sessions(instance_id,id);
CREATE UNIQUE INDEX player_sessions_active ON player_sessions(instance_id,identity_key) WHERE ended_at IS NULL;
CREATE TABLE player_history_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 source_cursor INTEGER NOT NULL,
 observed_at TEXT NOT NULL,
 kind TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 gap INTEGER NOT NULL DEFAULT 0,
 UNIQUE(instance_id,source_cursor)
);
CREATE INDEX player_history_events_retention ON player_history_events(instance_id,observed_at);
-- +goose Down
DROP TABLE player_history_events;
DROP TABLE player_sessions;
DROP TABLE player_snapshots;
