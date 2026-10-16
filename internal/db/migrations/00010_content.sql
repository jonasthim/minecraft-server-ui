-- +goose Up
CREATE TABLE content_items(instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,id TEXT NOT NULL,manifest_json TEXT NOT NULL,PRIMARY KEY(instance_id,id));
CREATE TABLE content_stages(id TEXT PRIMARY KEY,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,actor_id INTEGER NOT NULL,filename TEXT NOT NULL,sha256 TEXT NOT NULL,size INTEGER NOT NULL,created_at TEXT NOT NULL);
CREATE TABLE content_previews(id TEXT PRIMARY KEY,instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,actor_id INTEGER NOT NULL,private_json TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'preview',created_at TEXT NOT NULL);
CREATE TABLE content_journals(instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,preview_id TEXT NOT NULL,private_json TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('applying','committed','blocked','rolled_back')));
-- +goose Down
DROP TABLE content_journals;
DROP TABLE content_previews;
DROP TABLE content_stages;
DROP TABLE content_items;
