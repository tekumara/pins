# pins

Save your Pinboard bookmarks to a SQLite database. A single static binary with no cgo.

## Install

Download a release from the releases page, or build from a checkout with the Go version in `go.mod`:

```sh
go build -o pins .
```

## Usage

```sh
# 1. Save your API token (find it at https://pinboard.in/settings/password).
#    Paste the whole thing, e.g. yourname:XXXXXXXX.
pins auth                  # writes auth.json (mode 0600)

# 2. Fetch everything into a database.
pins import pinboard.db

# 3. Later, fetch only what is new.
pins import pinboard.db --since

# Or fetch everything created after a given date (UTC).
pins import pinboard.db --since-date 2024-01-01
```

Flags can appear before or after the database path. `-a/--auth` selects a different token file.

`--since` and `--since-date` cannot be used together. If the database has no posts yet, `--since` fetches everything.

Pinboard allows `posts/all` once every five minutes, so wait between runs.

## Output

A `posts` table, keyed on `hash`. Re-running updates existing rows, so it never duplicates.

| column        | type    | notes                                        |
|---------------|---------|----------------------------------------------|
| `hash`        | TEXT PK | Pinboard's bookmark ID                       |
| `href`        | TEXT    | URL                                          |
| `description` | TEXT    | title                                        |
| `extended`    | TEXT    | notes                                        |
| `meta`        | TEXT    | change-detection signature                   |
| `time`        | TEXT    | UTC, e.g. `2019-11-06T22:50:31+00:00`        |
| `shared`      | INTEGER | 1 if public                                  |
| `toread`      | INTEGER | 1 if marked to read                          |
| `tags`        | TEXT    | JSON array, e.g. `["fish", "sheep"]`         |

## Development

```sh
go test -race ./...
go vet ./...
staticcheck ./...
goreleaser build --snapshot --clean   # cross-compile all release targets
```

Layout:

```
main.go                       entry point
internal/cli/                 commands and flag handling
internal/pinboard/            API client (posts/all, streamed)
internal/store/               SQLite schema, upsert, latest-time lookup
```

Tests use a fake Pinboard server and never call the live API, so they need no token.

## Inspiration

This project started as a port of [jacobian/pinboard-to-sqlite](https://github.com/jacobian/pinboard-to-sqlite).
