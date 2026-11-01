// Package store writes Pinboard posts into a SQLite database.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo

	"pins/internal/pinboard"
)

// storedTimeLayout is an ISO 8601/RFC3339 timestamp e.g. 2019-11-06T22:50:31+00:00.
// Times have no fractional seconds, so lexical max() still orders them correctly.
const storedTimeLayout = "2006-01-02T15:04:05-07:00"

const schema = `
CREATE TABLE IF NOT EXISTS posts (
    hash        TEXT PRIMARY KEY,
    href        TEXT,
    description TEXT,
    extended    TEXT,
    meta        TEXT,
    time        TEXT,
    shared      INTEGER,
    toread      INTEGER,
    tags        TEXT
);
CREATE INDEX IF NOT EXISTS posts_time ON posts (time);
`

const upsert = `
INSERT INTO posts (hash, href, description, extended, meta, time, shared, toread, tags)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (hash) DO UPDATE SET
    href        = excluded.href,
    description = excluded.description,
    extended    = excluded.extended,
    meta        = excluded.meta,
    time        = excluded.time,
    shared      = excluded.shared,
    toread      = excluded.toread,
    tags        = excluded.tags`

// Open opens (or creates) the SQLite database at path.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("opening database %s: %w", path, err)
	}
	return db, nil
}

// LatestTime returns the newest post time in the form the Pinboard API accepts
// for fromdt (UTC, trailing Z), or "" if there are no posts.
func LatestTime(ctx context.Context, db *sql.DB) (string, error) {
	var exists int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'posts'`,
	).Scan(&exists); err != nil {
		return "", err
	}
	if exists == 0 {
		return "", nil
	}

	var latest sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT max(time) FROM posts`).Scan(&latest); err != nil {
		return "", err
	}
	if !latest.Valid {
		return "", nil
	}
	t, err := time.Parse(time.RFC3339, latest.String)
	if err != nil {
		return "", fmt.Errorf("parsing stored post time %q: %w", latest.String, err)
	}
	return t.UTC().Format(time.RFC3339), nil
}

// Writer upserts posts inside a transaction. Create it with NewWriter.
type Writer struct {
	stmt  *sql.Stmt
	count int
}

// NewWriter creates the posts table if needed and prepares the upsert statement.
func NewWriter(ctx context.Context, tx *sql.Tx) (*Writer, error) {
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	stmt, err := tx.PrepareContext(ctx, upsert)
	if err != nil {
		return nil, err
	}
	return &Writer{stmt: stmt}, nil
}

// Upsert inserts p, or updates the existing row with the same hash.
func (w *Writer) Upsert(p pinboard.Post) error {
	t, err := time.Parse(time.RFC3339, p.Time)
	if err != nil {
		return fmt.Errorf("post %s: parsing time %q: %w", p.Hash, p.Time, err)
	}
	tags, err := formatTags(strings.Fields(p.Tags))
	if err != nil {
		return err
	}

	if _, err := w.stmt.Exec(
		p.Hash,
		p.Href,
		p.Description,
		p.Extended,
		p.Meta,
		t.UTC().Format(storedTimeLayout),
		boolInt(p.Shared == "yes"),
		boolInt(p.ToRead == "yes"),
		tags,
	); err != nil {
		return fmt.Errorf("saving post %s: %w", p.Hash, err)
	}
	w.count++
	return nil
}

// Count returns the number of posts upserted so far.
func (w *Writer) Count() int { return w.count }

// Close releases the prepared statement.
func (w *Writer) Close() error { return w.stmt.Close() }

// formatTags renders tags as a stringified JSON array with ", " separators
func formatTags(tags []string) (string, error) {
	parts := make([]string, len(tags))
	for i, tag := range tags {
		q, err := json.Marshal(tag)
		if err != nil {
			return "", err
		}
		parts[i] = string(q)
	}
	return "[" + strings.Join(parts, ", ") + "]", nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
