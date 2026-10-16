-- +goose Up
CREATE TABLE metric_samples(instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,slot INTEGER NOT NULL,sample_json TEXT NOT NULL,PRIMARY KEY(instance_id,slot));
CREATE TABLE metric_hours(instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,slot INTEGER NOT NULL,aggregate_json TEXT NOT NULL,PRIMARY KEY(instance_id,slot));
-- +goose Down
DROP TABLE metric_hours;
DROP TABLE metric_samples;
