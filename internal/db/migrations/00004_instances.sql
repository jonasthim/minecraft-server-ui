-- +goose Up
CREATE TABLE instance_settings (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 eula_accepted INTEGER NOT NULL DEFAULT 0 CHECK(eula_accepted IN (0,1)),
 eula_accepted_by INTEGER,
 eula_accepted_at TEXT,
 ownership_token TEXT NOT NULL DEFAULT '',
 provisioning TEXT NOT NULL DEFAULT 'not_installed'
);
INSERT INTO instance_settings(instance_id) SELECT id FROM instances;
CREATE TABLE instance_mutations (
 instance_id TEXT PRIMARY KEY,
 kind TEXT NOT NULL CHECK(kind = 'delete'),
 staged_name TEXT NOT NULL UNIQUE,
 ownership_token TEXT NOT NULL
);
-- +goose Down
DROP TABLE instance_mutations;
DROP TABLE instance_settings;
