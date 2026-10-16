-- +goose Up
CREATE TABLE lifecycle_installations (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 installation_json TEXT NOT NULL
);
CREATE TABLE lifecycle_generations (
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 installation_json TEXT NOT NULL,
 PRIMARY KEY(instance_id,generation)
);
CREATE TABLE lifecycle_activations (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 generation TEXT NOT NULL,
 installation_json TEXT NOT NULL
);
CREATE TABLE lifecycle_previews (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 revision TEXT NOT NULL,
 original_selection_json TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 plan_json TEXT NOT NULL
);
CREATE TABLE lifecycle_autostart (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 desired INTEGER NOT NULL CHECK(desired IN(0,1)),
 applied INTEGER CHECK(applied IN(0,1)),
 error TEXT NOT NULL DEFAULT ''
);
-- +goose Down
DROP TABLE lifecycle_autostart;
DROP TABLE lifecycle_previews;
DROP TABLE lifecycle_activations;
DROP TABLE lifecycle_generations;
DROP TABLE lifecycle_installations;
