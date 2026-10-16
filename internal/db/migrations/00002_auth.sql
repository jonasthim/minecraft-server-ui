-- +goose Up
-- Authoritative data model. Timestamps are RFC3339 UTC strings.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,            -- lowercase
    display_name  TEXT NOT NULL DEFAULT '',
    email         TEXT NOT NULL DEFAULT '',
    password_hash TEXT,                            -- NULL for OIDC-only accounts
    role          TEXT NOT NULL CHECK (role IN ('viewer','operator','admin')),
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE user_identities (
    provider   TEXT NOT NULL,                      -- issuer URL
    subject    TEXT NOT NULL,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (provider, subject)
);
CREATE INDEX idx_user_identities_user ON user_identities(user_id);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,                 -- sha256 hex of the cookie token
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    ip           TEXT NOT NULL DEFAULT '',
    user_agent   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_sessions_user ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);

CREATE TABLE settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    doc        TEXT NOT NULL,                      -- JSON domain.Settings
    updated_at TEXT NOT NULL
);

CREATE TABLE audit_log (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           TEXT NOT NULL,
    user_id      INTEGER,
    username     TEXT NOT NULL DEFAULT '',
    action       TEXT NOT NULL,
    instance_id  TEXT NOT NULL DEFAULT '',
    target       TEXT NOT NULL DEFAULT '',
    details_json TEXT NOT NULL DEFAULT '{}',
    ip           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_audit_ts ON audit_log(ts DESC);
CREATE INDEX idx_audit_instance ON audit_log(instance_id, ts DESC);

CREATE TABLE api_tokens (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    hash         TEXT NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_used_at TEXT,
    expires_at   TEXT
);
CREATE INDEX idx_api_tokens_user ON api_tokens(user_id);

ALTER TABLE api_tokens ADD COLUMN scopes TEXT NOT NULL DEFAULT '["read"]';
CREATE TABLE oidc_flows(hash TEXT PRIMARY KEY, expires_at TEXT NOT NULL);
CREATE TABLE auth_bootstrap (id INTEGER PRIMARY KEY CHECK(id=1));
-- +goose StatementBegin
CREATE TRIGGER protect_last_admin_update BEFORE UPDATE OF role,disabled ON users WHEN OLD.role='admin' AND OLD.disabled=0 AND (NEW.role!='admin' OR NEW.disabled!=0) AND (SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0)=1 BEGIN SELECT RAISE(ABORT,'last_admin'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER protect_last_admin_delete BEFORE DELETE ON users WHEN OLD.role='admin' AND OLD.disabled=0 AND (SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0)=1 BEGIN SELECT RAISE(ABORT,'last_admin'); END;
-- +goose StatementEnd
-- +goose Down
DROP TRIGGER protect_last_admin_delete;
DROP TRIGGER protect_last_admin_update;
DROP TABLE auth_bootstrap;
DROP TABLE oidc_flows;
DROP TABLE api_tokens;
DROP TABLE audit_log;
DROP TABLE settings;
DROP TABLE sessions;
DROP TABLE user_identities;
DROP TABLE users;
