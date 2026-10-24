// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

// migrations are applied in order; never edit one that has been applied.
var migrations = []string{
	// 1: games, accounts, upload credentials, game managers, uploaded
	// contents, and the upload history.
	`
CREATE TABLE games (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	code      TEXT    NOT NULL UNIQUE,
	is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))
);

-- One account per email, for uploaders and managers alike. hashed_secret is
-- a bcrypt hash; an account without one cannot use the API.
CREATE TABLE accounts (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	email         TEXT    NOT NULL UNIQUE CHECK (email = lower(email)),
	hashed_secret TEXT,
	is_active     INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1))
);

CREATE TABLE game_uploaders (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id            INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id         INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	hashed_credentials BLOB    NOT NULL UNIQUE,
	is_active          INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);

CREATE INDEX game_uploaders_by_account ON game_uploaders (account_id);

CREATE TABLE game_managers (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);

CREATE INDEX game_managers_by_account ON game_managers (account_id);

-- Uploaded file bodies (without the header line), shared by every upload of
-- identical bytes.
-- created_at is when the content first arrived.
CREATE TABLE upload_blobs (
	content_hash TEXT NOT NULL PRIMARY KEY CHECK (length(content_hash) = 64),
	contents     BLOB NOT NULL CHECK (length(contents) BETWEEN 1 AND 65536),
	created_at   TEXT NOT NULL
);

-- email is the uploader's email when the upload arrived, lowercased like
-- every stored email.
CREATE TABLE uploads (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	uploader_id  INTEGER NOT NULL REFERENCES game_uploaders (id) ON DELETE RESTRICT,
	email        TEXT    NOT NULL CHECK (email = lower(email)),
	turn_no      TEXT    NOT NULL,
	content_hash TEXT    NOT NULL REFERENCES upload_blobs (content_hash) ON DELETE RESTRICT,
	uploaded_at  TEXT    NOT NULL
);

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
