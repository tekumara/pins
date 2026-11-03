# Contributing

## Development

Use the Makefile to build the CLI and run checks:

```sh
make             # build ./pins for this machine
make check       # vet, staticcheck, test, and build
make test        # go test -race
make build-darwin build-linux build-windows   # binaries in dist/
make tidy        # go mod tidy
```

cgo is enabled for darwin only. Linux and Windows builds use `CGO_ENABLED=0`. The darwin build cannot be cross-compiled from Linux or Windows because it needs the macOS SDK.

The GoReleaser config (`.goreleaser.yaml`) sets the same tags, so release binaries match `make`.

## Project layout

```text
main.go                       entry point
internal/cli/                 commands and flag handling
internal/tokenstore/          token storage: keyring backends, file passphrase
internal/keyringtest/         in-memory keyring fakes for tests
internal/pinboard/            API client (posts/all, streamed)
internal/store/               SQLite schema, upsert, latest-time lookup
```

## Tests

Tests use a fake Pinboard server and never call the live API. Keyring tests use in-memory fakes and never touch a real Keychain, Credential Manager, or Secret Service. The one test that uses the real encrypted-file backend works in a temporary directory.
