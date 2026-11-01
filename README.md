# pins

Save your Pinboard bookmarks to a SQLite database. Linux and Windows builds are static binaries. macOS builds use cgo to reach the Keychain.

## Install

Download a release from the releases page, or build from a checkout with the Go version in `go.mod`:

```sh
go build -o pins .
```

On macOS, building needs a C compiler, which the Xcode command line tools provide. Linux and Windows builds need none.

## Usage

```sh
# 1. Save your API token (find it at https://pinboard.in/settings/password).
#    Paste the whole thing, e.g. yourname:XXXXXXXX.
pins auth set

# 2. Fetch everything into the default database.
pins import

# 3. Later, fetch only what is new.
pins import --since

# Or fetch everything created after a given date (UTC).
pins import --since-date 2024-01-01

# Search titles, notes, URLs and tags.
pins search "sqlite AND indexing"
```

Both commands use `~/.local/state/pins/bookmarks.db` by default. Set `PINS_DATABASE` to use a different file, for example:

```sh
PINS_DATABASE=/path/to/bookmarks.db pins import
PINS_DATABASE=/path/to/bookmarks.db pins search "sqlite AND indexing"
```

Search flags can appear before or after the query.

`--since` and `--since-date` cannot be used together. If the database has no posts yet, `--since` fetches everything.

`pins search QUERY` uses SQLite FTS5 query syntax and returns up to 20 results as tab-separated rows with a header. Use `--limit N` to change the result count. For example, `"distributed systems"` searches for a phrase, `sqlite*` matches a prefix, and `tags:golang` searches only tags. Search ranks title matches above tags, notes and URLs. The first search builds and backfills the index for an existing database; imports keep it up to date.

Pinboard allows `posts/all` once every five minutes, so wait between runs.

### Where the token is stored

`pins` keeps the token in the first of these that works on your machine:

| Platform | Store |
|---|---|
| macOS | Keychain |
| Windows | Credential Manager |
| Linux with a desktop session | Secret Service (GNOME Keyring, KWallet) |
| Anywhere else | An encrypted file in `pins/keyring` under your config directory, protected by a passphrase you choose |

`pins import` reads the token from the store. It never reads `auth.json`.

```sh
pins auth status   # shows the username and store; exits 1 if none
pins auth unset    # deletes the token from every store that holds it
```

`pins auth set` reads the token from a hidden prompt. In a script, pipe it in as one line: `echo 'yourname:XXXXXXXX' | pins auth set`. Set writes to the store that is in use, and removes any copy from the others. With the encrypted file, you enter the passphrase twice. `auth status` and every later `import` ask for it once when they need to read the token.

Earlier versions saved the token to `auth.json` and took `-a/--auth`. Those are gone. `pins` does not read or delete `auth.json`. To finish moving, run `pins auth set`, then delete `auth.json`.

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
make             # build ./pins for this machine
make check       # vet, staticcheck, test, and build
make test        # go test -race
make build-darwin build-linux build-windows   # binaries in dist/
make tidy        # go mod tidy
```

The Makefile passes `-tags` to every build and check. The tags leave out the keyring backends pins does not use: 1Password, Proton Pass, `pass`, and `passage`. A stripped binary drops from about 26 MB to about 11 MB. The Keychain, Credential Manager, Secret Service, and file backends stay in. The `keyctl`, `kwallet`, and `winhello` backends stay in too, because the keyring library has no tag for them.

cgo is on for darwin only. Linux and Windows builds use `CGO_ENABLED=0`. The darwin build cannot be cross-compiled from Linux or Windows, because it needs the macOS SDK.

The goreleaser config (`.goreleaser.yaml`) sets the same tags, so release binaries match `make`.

Layout:

```
main.go                       entry point
internal/cli/                 commands and flag handling
internal/tokenstore/          token storage: keyring backends, file passphrase
internal/keyringtest/         in-memory keyring fakes for tests
internal/pinboard/            API client (posts/all, streamed)
internal/store/               SQLite schema, upsert, latest-time lookup
```

Tests use a fake Pinboard server and never call the live API. Keyring tests use in-memory fakes and never touch a real Keychain, Credential Manager, or Secret Service. The one test that uses the real encrypted-file backend works in a temp directory.

## Inspiration

This project started as a port of [jacobian/pinboard-to-sqlite](https://github.com/jacobian/pinboard-to-sqlite).
