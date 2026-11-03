package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tekumara/pins/internal/pinboard"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "pinboard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// save runs posts through a Writer in one transaction, as the CLI does.
func save(t *testing.T, db *sql.DB, posts ...pinboard.Post) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	w, err := NewWriter(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	for _, p := range posts {
		if err := w.Upsert(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func post(hash, desc, shared, toread, tags string) pinboard.Post {
	return pinboard.Post{
		Href: "https://example.com/" + hash, Description: desc, Extended: "ext", Meta: "m",
		Hash: hash, Time: "2019-11-06T22:50:31Z", Shared: shared, ToRead: toread, Tags: tags,
	}
}

func TestUpsertStoresRows(t *testing.T) {
	db := openTemp(t)
	save(t, db,
		post("h1", "An Example", "yes", "no", "fish sheep goats"),
		post("h2", "Example 2", "no", "yes", "elephants  goats\tlizards"),
	)

	rows, err := db.Query(`SELECT hash, description, shared, toread, tags, time FROM posts ORDER BY hash`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		Hash, Desc, Tags, Time string
		Shared, ToRead         int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Hash, &r.Desc, &r.Shared, &r.ToRead, &r.Tags, &r.Time); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []row{
		{Hash: "h1", Desc: "An Example", Shared: 1, ToRead: 0, Tags: `["fish", "sheep", "goats"]`, Time: "2019-11-06T22:50:31+00:00"},
		{Hash: "h2", Desc: "Example 2", Shared: 0, ToRead: 1, Tags: `["elephants", "goats", "lizards"]`, Time: "2019-11-06T22:50:31+00:00"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", got, want)
	}
}

func TestUpsertUpdatesExistingHash(t *testing.T) {
	db := openTemp(t)
	save(t, db, post("h1", "old title", "no", "no", "a"))
	save(t, db, post("h1", "new title", "yes", "yes", "b c"))

	var count int
	var desc, tags string
	var shared, toread int
	if err := db.QueryRow(`SELECT count(*) FROM posts`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT description, shared, toread, tags FROM posts WHERE hash = 'h1'`).
		Scan(&desc, &shared, &toread, &tags); err != nil {
		t.Fatal(err)
	}
	if count != 1 || desc != "new title" || shared != 1 || toread != 1 || tags != `["b", "c"]` {
		t.Fatalf("count=%d desc=%q shared=%d toread=%d tags=%s", count, desc, shared, toread, tags)
	}
}

func TestEmptyTagsAreEmptyJSONArray(t *testing.T) {
	db := openTemp(t)
	save(t, db, post("h1", "d", "no", "no", "   "))

	var tags string
	if err := db.QueryRow(`SELECT tags FROM posts WHERE hash = 'h1'`).Scan(&tags); err != nil {
		t.Fatal(err)
	}
	var parsed []string
	if err := json.Unmarshal([]byte(tags), &parsed); err != nil || len(parsed) != 0 || tags != "[]" {
		t.Fatalf("tags = %q, want []", tags)
	}
}

func TestBadTimeIsAnError(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	w, err := NewWriter(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	p := post("h1", "d", "no", "no", "")
	p.Time = "yesterday"
	if err := w.Upsert(p); err == nil {
		t.Fatal("expected error for unparseable time")
	}
}

func TestSearchBackfillsExistingRowsAndTracksUpserts(t *testing.T) {
	db := openTemp(t)
	_, err := db.Exec(`
CREATE TABLE posts (
    hash TEXT PRIMARY KEY, href TEXT, description TEXT, extended TEXT, meta TEXT,
    time TEXT, shared INTEGER, toread INTEGER, tags TEXT
);
INSERT INTO posts (hash, href, description, extended, meta, time, shared, toread, tags)
VALUES ('h1', 'https://example.com/h1', 'legacy comet', 'old notes', 'm',
        '2019-11-06T22:50:31+00:00', 1, 0, '["older"]')`)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	got, err := Search(ctx, db, "legacy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Hash != "h1" || got[0].Description != "legacy comet" {
		t.Fatalf("backfilled search = %+v, want the existing bookmark", got)
	}

	save(t, db, post("h1", "new nebula", "no", "no", "golang"))
	got, err = Search(ctx, db, "legacy", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("old title still matches after upsert: %+v", got)
	}
	got, err = Search(ctx, db, "nebula", 10)
	if err != nil || len(got) != 1 || got[0].Hash != "h1" {
		t.Fatalf("updated title search = %+v, %v; want h1", got, err)
	}
	got, err = Search(ctx, db, "tags:golang", 10)
	if err != nil || len(got) != 1 || got[0].Hash != "h1" {
		t.Fatalf("tag search = %+v, %v; want h1", got, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Upsert(post("h1", "rolledback quartz", "no", "no", "")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	got, err = Search(ctx, db, "quartz", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("rolled-back title search = %+v, %v; want no matches", got, err)
	}
	got, err = Search(ctx, db, "nebula", 10)
	if err != nil || len(got) != 1 || got[0].Hash != "h1" {
		t.Fatalf("search after rollback = %+v, %v; want h1", got, err)
	}
}

func TestSearchRanksTitleMatchesAheadOfNotes(t *testing.T) {
	db := openTemp(t)
	titleMatch := post("title", "quartz", "no", "no", "")
	titleMatch.Extended = "ordinary notes"
	notesMatch := post("notes", "ordinary title", "no", "no", "")
	notesMatch.Extended = "quartz"
	save(t, db, titleMatch, notesMatch)

	got, err := Search(context.Background(), db, "quartz", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Hash != "title" {
		t.Fatalf("search order = %+v, want title match before notes match", got)
	}
}

func TestLatestTime(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	if got, err := LatestTime(ctx, db); err != nil || got != "" {
		t.Fatalf("empty DB: got %q, %v; want \"\"", got, err)
	}

	// Create the table, but leave it empty.
	save(t, db)
	if got, err := LatestTime(ctx, db); err != nil || got != "" {
		t.Fatalf("empty table: got %q, %v; want \"\"", got, err)
	}

	older := post("h1", "d", "no", "no", "")
	older.Time = "2018-01-01T00:00:00Z"
	newer := post("h2", "d", "no", "no", "")
	newer.Time = "2019-11-06T22:50:31Z"
	save(t, db, older, newer)

	got, err := LatestTime(ctx, db)
	if err != nil || got != "2019-11-06T22:50:31Z" {
		t.Fatalf("LatestTime = %q, %v; want 2019-11-06T22:50:31Z", got, err)
	}
}
