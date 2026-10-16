-- +goose Up
CREATE TABLE instances (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 flavor TEXT NOT NULL CHECK(flavor IN ('vanilla','paper','fabric','forge','neoforge')),
 minecraft_version TEXT NOT NULL,
 loader_version TEXT NOT NULL DEFAULT '',
 java_major INTEGER NOT NULL CHECK(java_major >= 8),
 java_runtime_id TEXT NOT NULL DEFAULT '',
 memory_mib INTEGER NOT NULL CHECK(memory_mib >= 256),
 game_port INTEGER NOT NULL UNIQUE CHECK(game_port BETWEEN 1 AND 65535),
 config_json TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
 updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE TABLE process_identities (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 identity_json TEXT NOT NULL
);
CREATE TABLE artifacts (
 id TEXT PRIMARY KEY,
 instance_id TEXT REFERENCES instances(id) ON DELETE CASCADE,
 artifact_json TEXT NOT NULL
);
CREATE TABLE jobs (
 id TEXT PRIMARY KEY,
 instance_id TEXT REFERENCES instances(id) ON DELETE CASCADE,
 kind TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','running','succeeded','failed','cancelled')),
 phase TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL,
 started_at TEXT,
 finished_at TEXT
);
CREATE INDEX jobs_instance_created ON jobs(instance_id,created_at);
CREATE UNIQUE INDEX jobs_one_active_instance ON jobs(instance_id) WHERE state = 'running';
-- +goose Down
DROP TABLE jobs;
DROP TABLE artifacts;
DROP TABLE process_identities;
DROP TABLE instances;
