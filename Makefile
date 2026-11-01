# Build and check pins. `make` with no target builds a native binary.

BINARY      := pins
DIST        := dist
GO          ?= go
STATICCHECK ?= staticcheck

# Keyring backends that pins never uses are compiled out. Tags from the opt-out
# table in github.com/byteness/keyring's README:
#   keyring_no1password   1Password (op, op-connect, op-desktop); drops the 1Password SDK and wazero
#   keyring_nopass        pass
#   keyring_nopassage     passage
#   keyring_noprotonpass  Proton Pass
# Not excluded: keychain, wincred and secret-service (used), and file (the fallback).
# keyctl, kwallet and winhello have no opt-out tag in keyring v1.14.1, so they stay in.
TAGS := keyring_no1password,keyring_nopass,keyring_nopassage,keyring_noprotonpass

# cgo is needed only for the macOS Keychain (go-keychain, touchid-go).
cgo = $(if $(filter darwin,$(1)),1,0)

HOST_OS := $(shell $(GO) env GOOS)

.PHONY: build build-darwin build-linux build-windows test vet staticcheck tidy check clean

build:
	CGO_ENABLED=$(call cgo,$(HOST_OS)) $(GO) build -tags $(TAGS) -o $(BINARY) .

# Cross builds go to dist/ and use the host GOARCH unless you set GOARCH.
# The darwin build needs a Mac, because it uses cgo.
build-darwin build-linux build-windows: build-%:
	@mkdir -p $(DIST)
	GOOS=$* CGO_ENABLED=$(call cgo,$*) $(GO) build -tags $(TAGS) -o $(DIST)/$(BINARY)-$*$(if $(filter windows,$*),.exe,) .

test:
	$(GO) test -race -tags $(TAGS) ./...

vet:
	$(GO) vet -tags $(TAGS) ./...

staticcheck:
	$(STATICCHECK) -tags $(TAGS) ./...

tidy:
	$(GO) mod tidy

check: vet staticcheck test build

clean:
	rm -f $(BINARY)
	rm -f $(DIST)/$(BINARY)-*
