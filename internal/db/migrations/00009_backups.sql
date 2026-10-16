-- +goose Up
CREATE TABLE backups (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 job_id TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('staging','ready','failed','deleting')),
 metadata_json TEXT NOT NULL,
 manifest_json TEXT NOT NULL DEFAULT '{}',
 created_at TEXT NOT NULL,
 recovery TEXT NOT NULL DEFAULT ''
);
CREATE INDEX backups_instance_created ON backups(instance_id,created_at);
CREATE TABLE backup_leases (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 job_id TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('armed','released','dead','replaced','unresolved')),
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX backup_one_save_lease ON backup_leases(instance_id) WHERE state IN ('armed','unresolved');
CREATE TABLE backup_restores (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 backup_id TEXT NOT NULL,
 safety_id TEXT NOT NULL,
 job_id TEXT NOT NULL,
 state TEXT NOT NULL,
 journal_json TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX backup_one_restore ON backup_restores(instance_id) WHERE state != 'complete';
-- +goose Down
DROP TABLE backup_restores;
DROP TABLE backup_leases;
DROP TABLE backups;
