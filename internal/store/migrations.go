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
	// 2: API sessions. hashed_token is the SHA-256 of the bearer token that
	// /api/login issues; the token itself is never stored.
	`
CREATE TABLE api_sessions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id   INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	hashed_token BLOB    NOT NULL UNIQUE CHECK (length(hashed_token) = 32),
	created_at   TEXT    NOT NULL,
	expires_at   TEXT    NOT NULL
);

CREATE INDEX api_sessions_by_account ON api_sessions (account_id);
CREATE INDEX api_sessions_by_expiry ON api_sessions (expires_at);
`,
	// 3: systems, and the end of the header-line design. A game belongs to a
	// system and its code is unique only there. Upload credentials become
	// players, without credential hashes, and the uploads made under the
	// old design are dropped with their contents. Existing games move into a
	// system named "legacy". Every rebuilt table keeps its IDs and its
	// AUTOINCREMENT sequence, so no ID is ever reused.
	`
DROP VIEW active_uploaders;
DROP VIEW active_managers;
DROP TABLE uploads;
DROP TABLE upload_blobs;

CREATE TABLE systems (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	code TEXT    NOT NULL UNIQUE
);

INSERT INTO systems (code) SELECT 'legacy' WHERE EXISTS (SELECT 1 FROM games);

ALTER TABLE games RENAME TO games_v2;

CREATE TABLE games (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	system_id INTEGER NOT NULL REFERENCES systems (id) ON DELETE RESTRICT,
	code      TEXT    NOT NULL,
	is_active INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (system_id, code)
);

INSERT INTO games (id, system_id, code, is_active)
SELECT g.id, s.id, g.code, g.is_active FROM games_v2 g JOIN systems s ON s.code = 'legacy';

DELETE FROM sqlite_sequence WHERE name = 'games';
INSERT INTO sqlite_sequence (name, seq) SELECT 'games', seq FROM sqlite_sequence WHERE name = 'games_v2';

-- A player is an account that may use one game.
CREATE TABLE game_players (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);

INSERT INTO game_players (id, game_id, account_id, is_active)
SELECT id, game_id, account_id, is_active FROM game_uploaders;

DELETE FROM sqlite_sequence WHERE name = 'game_players';
INSERT INTO sqlite_sequence (name, seq) SELECT 'game_players', seq FROM sqlite_sequence WHERE name = 'game_uploaders';

DROP TABLE game_uploaders;

CREATE INDEX game_players_by_account ON game_players (account_id);

CREATE TABLE game_managers_v3 (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	is_active  INTEGER NOT NULL DEFAULT 1 CHECK (is_active IN (0, 1)),
	UNIQUE (game_id, account_id)
);

INSERT INTO game_managers_v3 (id, game_id, account_id, is_active)
SELECT id, game_id, account_id, is_active FROM game_managers;

DELETE FROM sqlite_sequence WHERE name = 'game_managers_v3';
INSERT INTO sqlite_sequence (name, seq) SELECT 'game_managers_v3', seq FROM sqlite_sequence WHERE name = 'game_managers';

DROP TABLE game_managers;
ALTER TABLE game_managers_v3 RENAME TO game_managers;

CREATE INDEX game_managers_by_account ON game_managers (account_id);

DROP TABLE games_v2;

-- Players allowed into their game: the row, its game, and its account are
-- active.
CREATE VIEW active_players AS
SELECT gp.id, gp.game_id, gp.account_id, a.email
FROM game_players gp
JOIN games g ON g.id = gp.game_id
JOIN accounts a ON a.id = gp.account_id
WHERE gp.is_active = 1 AND g.is_active = 1 AND a.is_active = 1;

-- Managers allowed to manage their game, active or not: the row and its
-- account are active.
CREATE VIEW active_managers AS
SELECT gm.id, gm.game_id, gm.account_id, a.email
FROM game_managers gm
JOIN accounts a ON a.id = gm.account_id
WHERE gm.is_active = 1 AND a.is_active = 1;
`,
	// 4: turns. A game's turns are numbered from 0 with no gaps. closed_at is
	// NULL while the turn is open.
	`
CREATE TABLE turns (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	game_id    INTEGER NOT NULL REFERENCES games (id) ON DELETE RESTRICT,
	turn_no    INTEGER NOT NULL CHECK (turn_no >= 0),
	created_at TEXT    NOT NULL,
	closed_at  TEXT,
	UNIQUE (game_id, turn_no)
);
`,
	// 5: files. file_contents holds each distinct file once, keyed by the
	// lowercase hex SHA-256 of its bytes. uploads records every upload: where
	// it was put, who put it there, and when. An upload with no game is at
	// its system's level, one with no turn at its game's, and one with no
	// player at its turn's. Rows of both tables are never deleted, and
	// contents are never rewritten.
	`
CREATE TABLE file_contents (
	sha256     TEXT    NOT NULL PRIMARY KEY CHECK (length(sha256) = 64),
	size       INTEGER NOT NULL CHECK (size > 0 AND size = length(contents)),
	contents   BLOB    NOT NULL,
	created_at TEXT    NOT NULL
);

CREATE TABLE uploads (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	system_id           INTEGER NOT NULL REFERENCES systems (id) ON DELETE RESTRICT,
	game_id             INTEGER REFERENCES games (id) ON DELETE RESTRICT,
	turn_id             INTEGER REFERENCES turns (id) ON DELETE RESTRICT,
	player_id           INTEGER REFERENCES game_players (id) ON DELETE RESTRICT,
	name                TEXT    NOT NULL,
	sha256              TEXT    NOT NULL REFERENCES file_contents (sha256) ON DELETE RESTRICT,
	uploader_account_id INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	-- The uploader's email as it was at the time of the upload.
	uploader_email      TEXT    NOT NULL,
	uploaded_at         TEXT    NOT NULL,
	-- NULL unless the upload was withdrawn.
	withdrawn_at        TEXT,
	CHECK (turn_id IS NULL OR game_id IS NOT NULL),
	CHECK (player_id IS NULL OR turn_id IS NOT NULL)
);

CREATE INDEX uploads_by_game ON uploads (game_id, id);
CREATE INDEX uploads_by_turn ON uploads (turn_id, player_id, id);
CREATE INDEX uploads_by_contents ON uploads (sha256);
`,
	// 6: the links mailed to an account so its owner can set a password: an
	// invitation for a new account, or a password reset. Only the SHA-256 of
	// a link's token is stored. A link sets a password once (used_at), and a
	// newer link for the same account cancels it (canceled_at). Rows are kept.
	`
CREATE TABLE account_links (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id   INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	kind         TEXT    NOT NULL CHECK (kind IN ('invitation', 'reset')),
	hashed_token BLOB    NOT NULL UNIQUE CHECK (length(hashed_token) = 32),
	created_at   TEXT    NOT NULL,
	expires_at   TEXT    NOT NULL,
	used_at      TEXT,
	canceled_at  TEXT
);

CREATE INDEX account_links_by_account ON account_links (account_id);
`,
	// 7: web sessions. hashed_token is the SHA-256 of the token in the
	// browser's cookie; the token itself is never stored. Each use moves
	// expires_at forward. A session is deleted when it is signed out, when
	// it has expired, and when its account's password is set.
	`
CREATE TABLE web_sessions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	account_id   INTEGER NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
	hashed_token BLOB    NOT NULL UNIQUE CHECK (length(hashed_token) = 32),
	created_at   TEXT    NOT NULL,
	last_used_at TEXT    NOT NULL,
	expires_at   TEXT    NOT NULL
);

CREATE INDEX web_sessions_by_account ON web_sessions (account_id);
CREATE INDEX web_sessions_by_expiry ON web_sessions (expires_at);
`,
	// 8: system-level files. The administrator adds them and is not an
	// account, so an upload with no game has no uploader account and an
	// empty uploader_email, and every other upload has both. SQLite cannot
	// drop NOT NULL from a column, so the table is rebuilt; nothing refers
	// to it. The copy keeps every ID, and the sequence is carried over first
	// so that no ID is ever reused.
	`
CREATE TABLE uploads_new (
	id                  INTEGER PRIMARY KEY AUTOINCREMENT,
	system_id           INTEGER NOT NULL REFERENCES systems (id) ON DELETE RESTRICT,
	game_id             INTEGER REFERENCES games (id) ON DELETE RESTRICT,
	turn_id             INTEGER REFERENCES turns (id) ON DELETE RESTRICT,
	player_id           INTEGER REFERENCES game_players (id) ON DELETE RESTRICT,
	name                TEXT    NOT NULL,
	sha256              TEXT    NOT NULL REFERENCES file_contents (sha256) ON DELETE RESTRICT,
	-- NULL for a system-level upload, which the administrator made.
	uploader_account_id INTEGER REFERENCES accounts (id) ON DELETE RESTRICT,
	-- The uploader's email as it was at the time of the upload; empty for
	-- the administrator.
	uploader_email      TEXT    NOT NULL,
	uploaded_at         TEXT    NOT NULL,
	-- NULL unless the upload was withdrawn.
	withdrawn_at        TEXT,
	CHECK (turn_id IS NULL OR game_id IS NOT NULL),
	CHECK (player_id IS NULL OR turn_id IS NOT NULL),
	CHECK ((game_id IS NULL) = (uploader_account_id IS NULL)),
	CHECK ((game_id IS NULL) = (uploader_email = ''))
);

INSERT INTO sqlite_sequence (name, seq) SELECT 'uploads_new', seq FROM sqlite_sequence WHERE name = 'uploads';

INSERT INTO uploads_new (id, system_id, game_id, turn_id, player_id, name, sha256, uploader_account_id, uploader_email, uploaded_at, withdrawn_at)
SELECT id, system_id, game_id, turn_id, player_id, name, sha256, uploader_account_id, uploader_email, uploaded_at, withdrawn_at FROM uploads;

DROP TABLE uploads;
ALTER TABLE uploads_new RENAME TO uploads;

CREATE INDEX uploads_by_game ON uploads (game_id, id);
CREATE INDEX uploads_by_turn ON uploads (turn_id, player_id, id);
CREATE INDEX uploads_by_contents ON uploads (sha256);
CREATE INDEX uploads_by_system ON uploads (system_id, id) WHERE game_id IS NULL;
`,
}
