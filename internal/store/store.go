// Package store keeps messages in SQLite and their raw source/attachments on disk:
// <data>/mailcatcher.db, <data>/raw/<id>.eml, <data>/att/<message id>/<attachment id>.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db  *sql.DB
	dir string
}

func Open(dataDir string) (*Store, error) {
	for _, d := range []string{dataDir, filepath.Join(dataDir, "raw"), filepath.Join(dataDir, "att")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, err
		}
	}
	dsn := "file:" + filepath.Join(dataDir, "mailcatcher.db") +
		"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, dir: dataDir}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Path resolves a path stored relative to the data dir.
func (s *Store) Path(rel string) string { return filepath.Join(s.dir, rel) }

// Append-only: each entry runs once, tracked by PRAGMA user_version.
var migrations = []string{
	`CREATE TABLE inboxes (
		id              INTEGER PRIMARY KEY,
		name            TEXT NOT NULL,
		slug            TEXT NOT NULL UNIQUE,
		domain          TEXT,
		format_template TEXT NOT NULL DEFAULT '{local}.{name}',
		assumed_tld     TEXT NOT NULL DEFAULT 'com',
		priority        INTEGER NOT NULL DEFAULT 0,
		is_default      INTEGER NOT NULL DEFAULT 0,
		api_key_hash    TEXT,
		created_at      INTEGER NOT NULL
	);
	INSERT INTO inboxes (name, slug, is_default, created_at) VALUES ('Default', 'default', 1, CAST(unixepoch('subsec') * 1000 AS INTEGER));

	CREATE TABLE messages (
		id            INTEGER PRIMARY KEY,
		inbox_id      INTEGER NOT NULL REFERENCES inboxes(id) ON DELETE CASCADE,
		direction     TEXT NOT NULL CHECK (direction IN ('in', 'out')),
		message_id    TEXT NOT NULL DEFAULT '',
		in_reply_to   TEXT NOT NULL DEFAULT '',
		refs          TEXT NOT NULL DEFAULT '[]',
		envelope_from TEXT NOT NULL DEFAULT '',
		envelope_to   TEXT NOT NULL DEFAULT '',
		from_name     TEXT NOT NULL DEFAULT '',
		from_addr     TEXT NOT NULL DEFAULT '',
		to_json       TEXT NOT NULL DEFAULT '[]',
		cc_json       TEXT NOT NULL DEFAULT '[]',
		subject       TEXT NOT NULL DEFAULT '',
		date          INTEGER,
		received_at   INTEGER NOT NULL,
		size          INTEGER NOT NULL,
		raw_path      TEXT NOT NULL DEFAULT '',
		raw_sha256    TEXT NOT NULL UNIQUE,
		text          TEXT NOT NULL DEFAULT '',
		html          TEXT NOT NULL DEFAULT '',
		links         TEXT NOT NULL DEFAULT '[]',
		codes         TEXT NOT NULL DEFAULT '[]',
		decoded_to    TEXT NOT NULL DEFAULT '',
		sent_by       INTEGER,
		parent_id     INTEGER REFERENCES messages(id) ON DELETE SET NULL
	);
	CREATE INDEX messages_inbox_received ON messages(inbox_id, received_at DESC);
	CREATE INDEX messages_envelope_to ON messages(envelope_to);
	CREATE INDEX messages_message_id ON messages(message_id);

	CREATE TABLE attachments (
		id           INTEGER PRIMARY KEY,
		message_id   INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
		filename     TEXT NOT NULL DEFAULT '',
		content_type TEXT NOT NULL DEFAULT '',
		size         INTEGER NOT NULL,
		path         TEXT NOT NULL DEFAULT '',
		cid          TEXT NOT NULL DEFAULT '',
		inline       INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX attachments_message ON attachments(message_id);`,
}

func (s *Store) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	if v > len(migrations) {
		return errors.New("database is newer than this binary")
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
