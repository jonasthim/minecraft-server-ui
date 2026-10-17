-- +goose Up
CREATE TABLE instance_pending_settings (
    instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL CHECK(typeof(revision) = 'integer' AND revision >= 1),
    draft_json TEXT,
    base_updated_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    updated_by INTEGER NOT NULL REFERENCES users(id)
);

-- +goose Down
DROP TABLE instance_pending_settings;
