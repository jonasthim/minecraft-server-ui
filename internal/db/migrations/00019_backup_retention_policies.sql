-- +goose Up
CREATE TABLE backup_retention_policies (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 keep_last INTEGER NOT NULL CHECK(typeof(keep_last)='integer' AND keep_last BETWEEN 0 AND 10000),
 keep_days INTEGER NOT NULL CHECK(typeof(keep_days)='integer' AND keep_days BETWEEN 0 AND 36500),
 revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision>=1),
 updated_at TEXT NOT NULL,
 updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL
);
-- Missing rows use the documented defaults (10 archives / 30 days), revision 0.
-- +goose Down
DROP TABLE backup_retention_policies;
