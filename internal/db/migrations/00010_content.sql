-- +goose Up
CREATE TABLE content_items(instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,id TEXT NOT NULL,manifest_json TEXT NOT NULL,PRIMARY KEY(instance_id,id));
CREATE TABLE content_stages(id TEXT PRIMARY KEY,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,actor_id INTEGER NOT NULL,filename TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE content_previews(id TEXT PRIMARY KEY,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,actor_id INTEGER NOT NULL,private_json TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'preview',job_id TEXT REFERENCES jobs(id) ON DELETE SET NULL,created_at TEXT NOT NULL);
CREATE TABLE content_journals(instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,preview_id TEXT NOT NULL,private_json TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('applying','committed','blocked','rolled_back')));
-- The private preview reference is the durable idempotency identity. This also
-- prevents a second admission if the manager dies after jobs.Enqueue commits
-- and before content_previews.job_id is bound.
CREATE UNIQUE INDEX content_one_live_preview_job ON jobs(instance_id,json_extract(CASE WHEN json_valid(payload) THEN payload ELSE '{}' END,'$.preview_id')) WHERE kind='content_mutation' AND state IN ('queued','running');
-- +goose Down
DROP INDEX content_one_live_preview_job;
DROP TABLE content_journals;
DROP TABLE content_previews;
DROP TABLE content_stages;
DROP TABLE content_items;
