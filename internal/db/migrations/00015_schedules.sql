-- +goose Up
CREATE TABLE schedules (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL REFERENCES users(id),
 definition_json TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN(0,1)),
 next_run_at TEXT NOT NULL DEFAULT '',
 disabled_reason TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE INDEX schedules_due ON schedules(enabled,next_run_at);
CREATE TABLE schedule_runs (
 id TEXT PRIMARY KEY,
 schedule_id TEXT NOT NULL REFERENCES schedules(id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 actor_id INTEGER NOT NULL,
 due_at TEXT NOT NULL,
 definition_json TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN('claimed','admitted','blocked','skipped_downtime','skipped_overlap')),
 job_id TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 UNIQUE(schedule_id,due_at)
);
CREATE INDEX schedule_runs_history ON schedule_runs(schedule_id,due_at DESC);
CREATE TABLE crash_recovery (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN(0,1)),
 actor_id INTEGER NOT NULL REFERENCES users(id),
 epoch INTEGER NOT NULL DEFAULT 0,
 last_generation TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
CREATE TABLE crash_attempts (
 id TEXT PRIMARY KEY,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 epoch INTEGER NOT NULL,
 actor_id INTEGER NOT NULL,
 generation TEXT NOT NULL,
 identity_json TEXT NOT NULL,
 due_at TEXT NOT NULL,
 admitted_at TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL CHECK(state IN('pending','queued','admitted','cancelled','blocked')),
 job_id TEXT NOT NULL DEFAULT '',
 reason TEXT NOT NULL DEFAULT ''
);
CREATE INDEX crash_attempts_window ON crash_attempts(instance_id,admitted_at);
CREATE TABLE scheduler_crash_events (
 id TEXT PRIMARY KEY, instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE, occurred_at TEXT NOT NULL, published INTEGER NOT NULL DEFAULT 0
);
-- +goose Down
DROP TABLE scheduler_crash_events;
DROP TABLE crash_attempts;
DROP TABLE crash_recovery;
DROP TABLE schedule_runs;
DROP TABLE schedules;
