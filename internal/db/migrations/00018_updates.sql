-- +goose Up
CREATE TABLE update_previews (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL,
 private_json TEXT NOT NULL,
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL
);
CREATE INDEX update_previews_instance ON update_previews(instance_id,created_at);
CREATE TABLE update_transactions (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 preview_id TEXT NOT NULL,
 job_id TEXT NOT NULL,
 actor_id INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('update','instance_rollback')),
 state TEXT NOT NULL CHECK(state IN ('prepared','publishing','committed','boot_attempted','complete','rolled_back','blocked','superseded')),
 backup_id TEXT NOT NULL,
 superseded_by TEXT REFERENCES update_transactions(id) DEFERRABLE INITIALLY DEFERRED,
 parent_transaction_id TEXT UNIQUE REFERENCES update_transactions(id) DEFERRABLE INITIALLY DEFERRED,
 journal_json TEXT NOT NULL,
 updated_at TEXT NOT NULL,
 CHECK((state='superseded')=(superseded_by IS NOT NULL)),
 CHECK(superseded_by IS NULL OR superseded_by!=id),
 CHECK(parent_transaction_id IS NULL OR (kind='instance_rollback' AND parent_transaction_id!=id)),
 CHECK(kind!='instance_rollback' OR parent_transaction_id IS NOT NULL)
);
CREATE UNIQUE INDEX update_one_active_transaction ON update_transactions(instance_id) WHERE state NOT IN ('complete','rolled_back','superseded');
CREATE UNIQUE INDEX update_one_live_preview_job ON jobs(instance_id,json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')) WHERE kind IN ('instance_update','instance_rollback') AND state IN ('queued','running');
-- Explicit world rollback consent and move checkpoints are backup-owned. These
-- rows are never scanned or executed by ordinary backup restore reconciliation.
CREATE TABLE backup_update_rollbacks (
 transaction_id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 job_id TEXT NOT NULL,
 actor_id INTEGER NOT NULL,
 backup_id TEXT NOT NULL,
 safety_id TEXT NOT NULL,
 consent_json TEXT NOT NULL,
 journal_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('prepared','replacing','data_replaced','committed','complete','blocked')),
 updated_at TEXT NOT NULL
);
CREATE UNIQUE INDEX backup_one_update_rollback ON backup_update_rollbacks(instance_id) WHERE state!='complete';
-- +goose Down
DROP INDEX backup_one_update_rollback;
DROP TABLE backup_update_rollbacks;
DROP INDEX update_one_live_preview_job;
DROP INDEX update_one_active_transaction;
DROP TABLE update_transactions;
DROP INDEX update_previews_instance;
DROP TABLE update_previews;
