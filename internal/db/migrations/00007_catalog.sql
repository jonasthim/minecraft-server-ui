-- +goose Up
-- Private key: never returned by any API; protected by manager database permissions.
CREATE TABLE catalog_settings (
 id INTEGER PRIMARY KEY CHECK(id=1),
 curseforge_key TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL
);
INSERT INTO catalog_settings(id,updated_at) VALUES(1,strftime('%Y-%m-%dT%H:%M:%SZ','now'));
-- +goose Down
DROP TABLE catalog_settings;
