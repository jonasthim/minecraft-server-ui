-- +goose Up
CREATE TABLE notification_channels(id TEXT PRIMARY KEY,label TEXT NOT NULL,provider TEXT NOT NULL,enabled INTEGER NOT NULL DEFAULT 0,events_json TEXT NOT NULL,allow_private INTEGER NOT NULL DEFAULT 0,private_json TEXT NOT NULL,event_floor INTEGER NOT NULL DEFAULT 0,updated_at INTEGER NOT NULL);
CREATE TABLE notification_events(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,instance_id TEXT NOT NULL,kind TEXT NOT NULL,operation TEXT NOT NULL,created_at INTEGER NOT NULL,processed INTEGER NOT NULL DEFAULT 0);
CREATE TABLE notification_deliveries(id INTEGER PRIMARY KEY AUTOINCREMENT,channel_id TEXT NOT NULL,event_id TEXT NOT NULL,provider TEXT NOT NULL,instance_id TEXT NOT NULL,event_kind TEXT NOT NULL,status TEXT NOT NULL CHECK(status IN ('queued','sending','sent','failed','unknown_after_crash')),attempts INTEGER NOT NULL DEFAULT 0,next_attempt INTEGER NOT NULL,created_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,safe_failure TEXT NOT NULL DEFAULT '',UNIQUE(channel_id,event_id));
CREATE INDEX notification_pending ON notification_deliveries(status,next_attempt,id);
-- The durable outbox observes terminal transitions, including late completion
-- of an old job. No private payload, error, requester or log text is copied.
-- Existing terminal jobs are deliberately not replayed at migration time.
-- +goose StatementBegin
CREATE TRIGGER notification_job_terminal AFTER UPDATE OF state ON jobs
WHEN NEW.instance_id IS NOT NULL AND NEW.instance_id!='' AND NEW.state IN ('succeeded','failed') AND OLD.state!=NEW.state AND NEW.kind IN ('backup_create','backup_restore','lifecycle','content_mutation','modpack_apply','update_apply')
BEGIN
 INSERT OR IGNORE INTO notification_events(id,instance_id,kind,operation,created_at) VALUES(NEW.id||':'||NEW.state,NEW.instance_id,CASE NEW.state WHEN 'failed' THEN 'job_failed' ELSE 'job_succeeded' END,NEW.kind,unixepoch());
END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER notification_job_terminal;
DROP TABLE notification_deliveries;
DROP TABLE notification_events;
DROP TABLE notification_channels;
