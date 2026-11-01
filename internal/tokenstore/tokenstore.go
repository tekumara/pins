// Package tokenstore keeps the Pinboard API token in the OS keyring. Where no
// OS keyring is available, it falls back to an encrypted file.
package tokenstore

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/byteness/keyring"
)

// ItemKey is the keyring entry that holds the token.
const ItemKey = "pinboard-token"

// Backends lists the backends in the order they are tried. The first one that
// opens is used.
var Backends = []keyring.BackendType{
	keyring.KeychainBackend,
	keyring.WinCredBackend,
	keyring.SecretServiceBackend,
	keyring.FileBackend,
}

// Opener opens a keyring that may use only the allowed backends, in order, as
// keyring.Open does with Config.AllowedBackends. Tests substitute a fake.
type Opener func(allowed []keyring.BackendType, passphrase keyring.PromptFunc) (keyring.Keyring, error)

var (
	// ErrNotStored means no backend holds the token.
	ErrNotStored = errors.New("no Pinboard token is stored")
	// ErrNoBackend means no backend opened.
	ErrNoBackend = errors.New("no keyring backend is available")
)

// Store reads and writes the token through the first backend that opens.
type Store struct {
	open     Opener
	dir      string                // directory of the encrypted file backend
	prompt   keyring.PromptFunc    // asks for the file passphrase
	backends []keyring.BackendType // tried in order; Backends, unless a test narrows it
}

// New returns a Store that tries the OS keyring first and an encrypted file in
// dir second. prompt asks for the file passphrase.
func New(open Opener, dir string, prompt keyring.PromptFunc) *Store {
	return &Store{open: open, dir: dir, prompt: nonEmpty(prompt), backends: Backends}
}

// backend is an open keyring together with the type it was opened as.
type backend struct {
	typ keyring.BackendType
	kr  keyring.Keyring
}

// Name returns the name shown to the user for b.
func (s *Store) Name(b keyring.BackendType) string {
	switch b {
	case keyring.KeychainBackend:
		return "macOS Keychain"
	case keyring.WinCredBackend:
		return "Windows Credential Manager"
	case keyring.SecretServiceBackend:
		return "Secret Service"
	case keyring.FileBackend:
		return "encrypted file in " + s.dir
	default:
		return string(b)
	}
}

// Get returns the stored token, or ErrNotStored. It reads only the backend that
// Set writes to.
func (s *Store) Get() (string, error) {
	b, err := s.active(s.prompt)
	if err != nil {
		return "", err
	}
	if b.typ == keyring.FileBackend && !s.dirExists() {
		// Get would create the directory while resolving the file name.
		return "", ErrNotStored
	}
	item, err := b.kr.Get(ItemKey)
	if missing(err) {
		return "", ErrNotStored
	}
	if err != nil {
		return "", fmt.Errorf("reading from %s: %w", s.Name(b.typ), err)
	}
	return string(item.Data), nil
}

// Set stores token in the backend that Get reads from, then deletes any copy
// from the other backends. The file passphrase is asked for twice.
//
// If the write succeeds but a stale copy cannot be deleted, Set returns the
// backend holding the new token together with the error.
func (s *Store) Set(token string) (keyring.BackendType, error) {
	pass := confirmed(s.prompt)
	b, err := s.active(pass)
	if err != nil {
		return "", err
	}
	item := keyring.Item{Key: ItemKey, Label: "pins Pinboard token", Data: []byte(token)}
	if err := b.kr.Set(item); err != nil {
		return "", fmt.Errorf("saving to %s: %w", s.Name(b.typ), err)
	}
	for _, other := range s.usable(pass) {
		if other.typ == b.typ {
			continue
		}
		if _, err := remove(other.kr); err != nil {
			return b.typ, fmt.Errorf("token saved to %s, but removing the old copy from %s failed: %w",
				s.Name(b.typ), s.Name(other.typ), err)
		}
	}
	return b.typ, nil
}

// Status returns the first backend that holds the token and its username, or
// ErrNotStored. It reads the token to extract the username, so an encrypted
// file backend asks for its passphrase when it contains a token. The token is
// never returned.
//
// If no backend holds the token and some backend failed to check, Status
// returns those failures instead of ErrNotStored.
func (s *Store) Status() (keyring.BackendType, string, error) {
	var errs []error
	for _, b := range s.usable(s.prompt) {
		token, held, err := tokenIfHeld(b.kr)
		if err != nil {
			errs = append(errs, fmt.Errorf("checking %s: %w", s.Name(b.typ), err))
			continue
		}
		if !held {
			continue
		}
		user, secret, ok := strings.Cut(token, ":")
		if !ok || user == "" || secret == "" {
			return "", "", fmt.Errorf("stored Pinboard token in %s has invalid username:TOKEN format", s.Name(b.typ))
		}
		return b.typ, user, nil
	}
	if len(errs) > 0 {
		return "", "", errors.Join(errs...)
	}
	return "", "", ErrNotStored
}

// Unset deletes the token from every usable backend. It tries every backend
// before returning, and returns the backends it removed the token from. Nothing
// stored is not an error.
func (s *Store) Unset() ([]keyring.BackendType, error) {
	var removed []keyring.BackendType
	var errs []error
	for _, b := range s.usable(s.prompt) {
		ok, err := remove(b.kr)
		if err != nil {
			errs = append(errs, fmt.Errorf("removing from %s: %w", s.Name(b.typ), err))
			continue
		}
		if ok {
			removed = append(removed, b.typ)
		}
	}
	return removed, errors.Join(errs...)
}

// active returns the backend that keyring.Open would choose from s.backends: the
// first one that opens on its own.
func (s *Store) active(pass keyring.PromptFunc) (backend, error) {
	for _, typ := range s.backends {
		if kr, err := s.open([]keyring.BackendType{typ}, pass); err == nil {
			return backend{typ, kr}, nil
		}
	}
	return backend{}, ErrNoBackend
}

// usable returns every backend in s.backends that opens on its own, in order.
// The file backend is left out while its directory does not exist. Checking or
// deleting from it would create the directory, and without the directory it
// cannot hold a token.
func (s *Store) usable(pass keyring.PromptFunc) []backend {
	var out []backend
	for _, typ := range s.backends {
		if typ == keyring.FileBackend && !s.dirExists() {
			continue
		}
		if kr, err := s.open([]keyring.BackendType{typ}, pass); err == nil {
			out = append(out, backend{typ, kr})
		}
	}
	return out
}

func (s *Store) dirExists() bool {
	info, err := os.Stat(s.dir)
	return err == nil && info.IsDir()
}

// tokenIfHeld returns the token if kr holds it. Metadata lets it skip reading
// the token when the item is absent, so encrypted files do not prompt unless
// they actually contain one.
func tokenIfHeld(kr keyring.Keyring) (string, bool, error) {
	_, err := kr.GetMetadata(ItemKey)
	if err != nil &&
		!errors.Is(err, keyring.ErrMetadataNotSupported) &&
		!errors.Is(err, keyring.ErrMetadataNeedsCredentials) {
		if missing(err) {
			return "", false, nil
		}
		return "", false, err
	}

	item, err := kr.Get(ItemKey)
	if missing(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(item.Data), true, nil
}

// holds reports whether kr has the token. It uses metadata where the backend
// offers it, because metadata never prompts for a passphrase.
func holds(kr keyring.Keyring) (bool, error) {
	_, err := kr.GetMetadata(ItemKey)
	if errors.Is(err, keyring.ErrMetadataNotSupported) || errors.Is(err, keyring.ErrMetadataNeedsCredentials) {
		_, err = kr.Get(ItemKey)
	}
	switch {
	case err == nil:
		return true, nil
	case missing(err):
		return false, nil
	default:
		return false, err
	}
}

// remove deletes the token from kr if kr holds it, and reports whether it did.
func remove(kr keyring.Keyring) (bool, error) {
	held, err := holds(kr)
	if err != nil || !held {
		return false, err
	}
	// Secret Service returns nil for a missing item, so the check above is what
	// tells us whether anything was removed.
	err = kr.Remove(ItemKey)
	if missing(err) {
		return false, nil
	}
	return err == nil, err
}

// missing reports whether err means the item is absent. Backends differ: the
// keyring package's sentinel, or a plain missing file for the file backend.
func missing(err error) bool {
	return errors.Is(err, keyring.ErrKeyNotFound) || errors.Is(err, fs.ErrNotExist)
}
