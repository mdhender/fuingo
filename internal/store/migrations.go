// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import "zombiezen.com/go/sqlite/sqlitemigration"

// migrations are applied in order; never edit one that has been applied.
var migrations = []string{
	// 1: games, upload credentials, uploaded contents, and the upload history.
	`
CREATE TABLE games (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	code      TEXT    NOT NULL UNIQUE,
	is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))
);

CREATE TABLE game_uploaders (
	game_id         INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	email           TEXT    NOT NULL CHECK (email = lower(email)),
	credential_hash BLOB    NOT NULL UNIQUE,
	is_active       INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	PRIMARY KEY (game_id, email)
);

-- Uploaded file bodies (without the header line), shared by every upload of
-- identical bytes.
-- created_at is when the content first arrived.
CREATE TABLE upload_blobs (
	content_hash TEXT NOT NULL PRIMARY KEY CHECK (length(content_hash) = 64),
	contents     BLOB NOT NULL CHECK (length(contents) BETWEEN 1 AND 65536),
	created_at   TEXT NOT NULL
);

CREATE TABLE uploads (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id      INTEGER NOT NULL,
	email        TEXT    NOT NULL,
	turn_no      TEXT    NOT NULL,
	content_hash TEXT    NOT NULL REFERENCES upload_blobs (content_hash) ON DELETE RESTRICT,
	uploaded_at  TEXT    NOT NULL,
	FOREIGN KEY (game_id, email) REFERENCES game_uploaders (game_id, email) ON DELETE RESTRICT
);

CREATE INDEX uploads_by_uploader ON uploads (game_id, email);
CREATE INDEX uploads_by_content ON uploads (content_hash);
`,
	// 2: API accounts. password_hash is a bcrypt hash.
	`
CREATE TABLE accounts (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	email         TEXT    NOT NULL UNIQUE CHECK (email = lower(email)),
	password_hash TEXT    NOT NULL
);
`,
	// 3: the games each API account manages.
	`
CREATE TABLE game_managers (
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	PRIMARY KEY (game_id, account_id)
);

CREATE INDEX game_managers_by_account ON game_managers (account_id);
`,
	// 4: managers can be disabled, like games and uploaders.
	`
ALTER TABLE game_managers ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1));
`,
	// 5: API accounts can be disabled.
	`
ALTER TABLE accounts ADD COLUMN is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1));
`,
	// 6: one account per email, for uploaders and managers alike. An account
	// may use the API only if it has a hashed_secret (a bcrypt hash).
	// game_uploaders and game_managers get integer IDs, and uploads refer to
	// their uploader by ID. Each table is rebuilt; copying the old table's
	// sqlite_sequence row first keeps IDs from being reused.
	`
CREATE TABLE new_accounts (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	email         TEXT    NOT NULL UNIQUE CHECK (email = lower(email)),
	hashed_secret TEXT,
	is_active     INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))
);
INSERT INTO sqlite_sequence (name, seq) SELECT 'new_accounts', seq FROM sqlite_sequence WHERE name = 'accounts';
INSERT INTO new_accounts (id, email, hashed_secret, is_active)
SELECT id, email, password_hash, is_active FROM accounts ORDER BY id;
INSERT INTO new_accounts (email, hashed_secret, is_active)
SELECT DISTINCT email, NULL, 1 FROM game_uploaders
WHERE email NOT IN (SELECT email FROM accounts) ORDER BY email;

CREATE TABLE new_game_uploaders (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id            INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id         INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	hashed_credentials BLOB    NOT NULL UNIQUE,
	is_active          INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);
INSERT INTO new_game_uploaders (game_id, account_id, hashed_credentials, is_active)
SELECT u.game_id, a.id, u.credential_hash, u.is_active
FROM game_uploaders u JOIN new_accounts a ON a.email = u.email
ORDER BY u.rowid;

CREATE TABLE new_game_managers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);
INSERT INTO new_game_managers (game_id, account_id, is_active)
SELECT game_id, account_id, is_active FROM game_managers ORDER BY rowid;

-- email is the uploader's email when the upload arrived.
CREATE TABLE new_uploads (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	uploader_id  INTEGER NOT NULL REFERENCES game_uploaders (id) ON DELETE RESTRICT,
	email        TEXT    NOT NULL,
	turn_no      TEXT    NOT NULL,
	content_hash TEXT    NOT NULL REFERENCES upload_blobs (content_hash) ON DELETE RESTRICT,
	uploaded_at  TEXT    NOT NULL
);
INSERT INTO sqlite_sequence (name, seq) SELECT 'new_uploads', seq FROM sqlite_sequence WHERE name = 'uploads';
INSERT INTO new_uploads (id, uploader_id, email, turn_no, content_hash, uploaded_at)
SELECT up.id, gu.id, up.email, up.turn_no, up.content_hash, up.uploaded_at
FROM uploads up
JOIN new_accounts a ON a.email = up.email
JOIN new_game_uploaders gu ON gu.game_id = up.game_id AND gu.account_id = a.id
ORDER BY up.id;

DROP TABLE uploads;
DROP TABLE game_managers;
DROP TABLE game_uploaders;
DROP TABLE accounts;
ALTER TABLE new_accounts RENAME TO accounts;
ALTER TABLE new_game_uploaders RENAME TO game_uploaders;
ALTER TABLE new_game_managers RENAME TO game_managers;
ALTER TABLE new_uploads RENAME TO uploads;

CREATE INDEX game_uploaders_by_account ON game_uploaders (account_id);
CREATE INDEX game_managers_by_account ON game_managers (account_id);
CREATE INDEX uploads_by_uploader ON uploads (uploader_id);
CREATE INDEX uploads_by_content ON uploads (content_hash);

-- Uploaders allowed to upload: the row, its game, and its account are active.
CREATE VIEW active_uploaders AS
SELECT gu.id, gu.game_id, gu.account_id, a.email, gu.hashed_credentials
FROM game_uploaders gu
JOIN games g ON g.id = gu.game_id
JOIN accounts a ON a.id = gu.account_id
WHERE gu.is_active = 1 AND g.is_active = 1 AND a.is_active = 1;

-- Managers allowed to manage their game, active or not: the row and its
-- account are active.
CREATE VIEW active_managers AS
SELECT gm.id, gm.game_id, gm.account_id, a.email
FROM game_managers gm
JOIN accounts a ON a.id = gm.account_id
WHERE gm.is_active = 1 AND a.is_active = 1;
`,
}

// migrationOptions are the options for each migration, by index. The table
// rebuilds turn foreign keys off while they run.
var migrationOptions = []*sqlitemigration.MigrationOptions{
	5: {DisableForeignKeys: true},
}
