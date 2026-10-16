-- +goose Up
CREATE TABLE game_event_cursors (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 source_cursor INTEGER NOT NULL DEFAULT 0 CHECK(source_cursor >= 0),
 retention_floor INTEGER NOT NULL DEFAULT 0,
 availability TEXT NOT NULL DEFAULT '',
 coverage_json TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE game_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 source_key TEXT NOT NULL,
 source_cursor INTEGER NOT NULL CHECK(source_cursor >= 0),
 observed_at TEXT NOT NULL,
 event_json TEXT NOT NULL,
 UNIQUE(instance_id,source_key)
);
CREATE INDEX game_events_history ON game_events(instance_id,id);
CREATE INDEX game_events_retention ON game_events(instance_id,observed_at);
-- +goose Down
DROP TABLE game_events;
DROP TABLE game_event_cursors;
