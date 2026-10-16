-- +goose Up
-- Admission tombstones intentionally outlive job/history/instance retention.
-- A retired job ID may no longer be displayed, but its key can never be reused.
CREATE TABLE job_admissions (
 admission_key TEXT PRIMARY KEY,
 fingerprint TEXT NOT NULL,
 job_id TEXT NOT NULL UNIQUE,
 created_at TEXT NOT NULL
);
-- +goose Down
DROP TABLE job_admissions;
