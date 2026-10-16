-- +goose Up
CREATE TABLE modpack_baselines (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 baseline_json TEXT NOT NULL
);
CREATE TABLE modpack_stages (
 id TEXT PRIMARY KEY, instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL REFERENCES users(id), sha256 TEXT NOT NULL, size INTEGER NOT NULL CHECK(size>=0), created_at TEXT NOT NULL
);
CREATE TABLE modpack_archives (
 id TEXT PRIMARY KEY REFERENCES modpack_stages(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL REFERENCES users(id), binding_json TEXT NOT NULL,created_at TEXT NOT NULL
);
CREATE TABLE modpack_previews (
 id TEXT PRIMARY KEY, instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL REFERENCES users(id), private_json TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'preview', created_at TEXT NOT NULL
);
CREATE TABLE modpack_journals (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 preview_id TEXT NOT NULL, private_json TEXT NOT NULL, state TEXT NOT NULL
);
CREATE UNIQUE INDEX modpack_one_live_preview_job ON jobs(instance_id,json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')) WHERE kind='modpack_apply' AND state IN ('queued','running');
-- +goose Down
DROP INDEX modpack_one_live_preview_job;
DROP TABLE modpack_journals;
DROP TABLE modpack_previews;
DROP TABLE modpack_archives;
DROP TABLE modpack_stages;
DROP TABLE modpack_baselines;
