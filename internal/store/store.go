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

	"github.com/tekumara/pins/internal/pinboard"
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

const searchSchema = `
CREATE VIRTUAL TABLE IF NOT EXISTS posts_fts USING fts5(
    description, extended, href, tags,
    content = 'posts', content_rowid = 'rowid'
)`

const searchInsertTrigger = `
CREATE TRIGGER IF NOT EXISTS posts_fts_ai AFTER INSERT ON posts BEGIN
    INSERT INTO posts_fts(rowid, description, extended, href, tags)
    VALUES (new.rowid, new.description, new.extended, new.href, new.tags);
END`

const searchDeleteTrigger = `
CREATE TRIGGER IF NOT EXISTS posts_fts_ad AFTER DELETE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, description, extended, href, tags)
    VALUES ('delete', old.rowid, old.description, old.extended, old.href, old.tags);
END`

const searchUpdateTrigger = `
CREATE TRIGGER IF NOT EXISTS posts_fts_au AFTER UPDATE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, description, extended, href, tags)
    VALUES ('delete', old.rowid, old.description, old.extended, old.href, old.tags);
    INSERT INTO posts_fts(rowid, description, extended, href, tags)
    VALUES (new.rowid, new.description, new.extended, new.href, new.tags);
END`

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
	if err := ensureSearchIndex(ctx, tx); err != nil {
		return nil, fmt.Errorf("creating search index: %w", err)
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

// SearchResult is a bookmark returned by Search.
type SearchResult struct {
	Hash        string
	Description string
	Href        string
	Tags        string
	Time        string
}

// Search searches bookmark titles, notes, URLs and tags using FTS5 syntax.
// It creates and backfills the index on databases imported by older versions.
func Search(ctx context.Context, db *sql.DB, query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("search query is empty")
	}
	if limit < 1 {
		return nil, fmt.Errorf("search limit must be positive")
	}

	var postsExists int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'posts'`,
	).Scan(&postsExists); err != nil {
		return nil, err
	}
	if postsExists == 0 {
		return []SearchResult{}, nil
	}

	var indexExists int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'posts_fts'`,
	).Scan(&indexExists); err != nil {
		return nil, err
	}
	if indexExists == 0 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		if err := ensureSearchIndex(ctx, tx); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("creating search index: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("creating search index: %w", err)
		}
	}

	rows, err := db.QueryContext(ctx, `
SELECT p.hash, COALESCE(p.description, ''), COALESCE(p.href, ''),
       COALESCE(p.tags, '[]'), COALESCE(p.time, '')
FROM posts_fts
JOIN posts AS p ON p.rowid = posts_fts.rowid
WHERE posts_fts MATCH ?
ORDER BY bm25(posts_fts, 5.0, 1.0, 0.25, 2.0), p.time DESC
LIMIT ?`, query, limit)
	if err != nil {
		return nil, fmt.Errorf("searching bookmarks: %w", err)
	}
	defer rows.Close()

	results := make([]SearchResult, 0)
	for rows.Next() {
		var result SearchResult
		if err := rows.Scan(&result.Hash, &result.Description, &result.Href, &result.Tags, &result.Time); err != nil {
			return nil, fmt.Errorf("reading search results: %w", err)
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading search results: %w", err)
	}
	return results, nil
}

// ensureSearchIndex creates the external-content FTS5 table and its maintenance
// triggers. Existing rows are rebuilt only when the index is first created.
func ensureSearchIndex(ctx context.Context, tx *sql.Tx) error {
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'posts_fts'`,
	).Scan(&exists); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, searchSchema); err != nil {
		return err
	}
	for _, trigger := range []string{searchInsertTrigger, searchDeleteTrigger, searchUpdateTrigger} {
		if _, err := tx.ExecContext(ctx, trigger); err != nil {
			return err
		}
	}
	if exists == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO posts_fts(posts_fts) VALUES ('rebuild')`); err != nil {
			return err
		}
	}
	return nil
}

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
