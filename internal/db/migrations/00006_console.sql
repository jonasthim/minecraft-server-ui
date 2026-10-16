-- +goose Up
CREATE TABLE console_lines (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 created_at TEXT NOT NULL,
 message TEXT NOT NULL,
 gap INTEGER NOT NULL DEFAULT 0 CHECK(gap IN (0,1))
);
CREATE INDEX console_lines_instance_cursor ON console_lines(instance_id,id);
CREATE TABLE console_cursors (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 byte_offset INTEGER NOT NULL DEFAULT 0,
 boundary_hash TEXT NOT NULL DEFAULT '',
 pruned_before INTEGER NOT NULL DEFAULT 0
);
-- Every locally bound TCP protocol shares this canonical reservation table.
CREATE TABLE instance_tcp_ports (
 port INTEGER PRIMARY KEY CHECK(port BETWEEN 1 AND 65535),
 instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('game','management')),
 UNIQUE(instance_id,kind),
 UNIQUE(port,instance_id,kind)
);
INSERT INTO instance_tcp_ports(port,instance_id,kind) SELECT game_port,id,'game' FROM instances;
-- +goose StatementBegin
CREATE TRIGGER instance_game_port_insert AFTER INSERT ON instances BEGIN
 INSERT INTO instance_tcp_ports(port,instance_id,kind) VALUES(NEW.game_port,NEW.id,'game');
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER instance_game_port_update AFTER UPDATE OF game_port ON instances BEGIN
 UPDATE instance_tcp_ports SET port=NEW.game_port WHERE instance_id=NEW.id AND kind='game';
END;
-- +goose StatementEnd
CREATE TABLE instance_management (
 instance_id TEXT PRIMARY KEY REFERENCES instances(id) ON DELETE CASCADE,
 host TEXT NOT NULL CHECK(host='127.0.0.1'),
 port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
 kind TEXT NOT NULL DEFAULT 'management' CHECK(kind='management'),
 secret TEXT NOT NULL CHECK(length(secret)=40 AND secret NOT GLOB '*[^a-zA-Z0-9]*'),
 protocol TEXT NOT NULL CHECK(protocol IN ('1.0.0','2.0.0','3.0.0','3.1.0')),
 FOREIGN KEY(port,instance_id,kind) REFERENCES instance_tcp_ports(port,instance_id,kind) ON DELETE CASCADE
);
-- +goose Down
DROP TABLE instance_management;
DROP TRIGGER instance_game_port_update;
DROP TRIGGER instance_game_port_insert;
DROP TABLE instance_tcp_ports;
DROP TABLE console_cursors;
DROP TABLE console_lines;
