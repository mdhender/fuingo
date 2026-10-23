// Copyright (c) 2026 Michael D Henderson.
// SPDX-License-Identifier: AGPL-3.0-or-later

package store

// migrations are applied in order; never edit one that has been applied.
// The accounts and game_managers tables wait for the /api/ phase.
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

-- Uploaded file contents, shared by every upload of identical bytes.
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
	content_hash TEXT    NOT NULL REFERENCES upload_blobs (content_hash) ON DELETE RESTRICT,
	uploaded_at  TEXT    NOT NULL,
	FOREIGN KEY (game_id, email) REFERENCES game_uploaders (game_id, email) ON DELETE RESTRICT
);

CREATE INDEX uploads_by_uploader ON uploads (game_id, email);
CREATE INDEX uploads_by_content ON uploads (content_hash);
`,
}
