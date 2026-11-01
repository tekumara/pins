// Package keyringtest provides in-memory keyring backends for tests. No test
// using it touches a real Keychain, Credential Manager, Secret Service or file.
package keyringtest

import "github.com/byteness/keyring"

// Backend is an in-memory keyring backend of one type.
type Backend struct {
	Type keyring.BackendType
	// Items holds the stored values by key.
	Items map[string][]byte
	// Missing is returned for an absent item. The file backend returns
	// os.ErrNotExist, and the others return keyring.ErrKeyNotFound.
	Missing error
	// NoMetadata makes GetMetadata return keyring.ErrMetadataNotSupported, as
	// Credential Manager does.
	NoMetadata bool
	// NeedsPassphrase makes Get and Set call the passphrase prompt, as the file
	// backend does. Get calls it only when the item exists.
	NeedsPassphrase bool
	// MetadataErr, if set, is returned by GetMetadata, as a failing check would be.
	MetadataErr error
	// RemoveErr, if set, is returned by Remove, and nothing is removed.
	RemoveErr error
}

// NewBackend returns an empty backend of type t.
func NewBackend(t keyring.BackendType) *Backend {
	return &Backend{Type: t, Items: map[string][]byte{}, Missing: keyring.ErrKeyNotFound}
}

// Opener returns an opener that behaves like keyring.Open. It returns the first
// allowed backend that is among backends, or keyring.ErrNoAvailImpl if none is.
func Opener(backends ...*Backend) func([]keyring.BackendType, keyring.PromptFunc) (keyring.Keyring, error) {
	return func(allowed []keyring.BackendType, passphrase keyring.PromptFunc) (keyring.Keyring, error) {
		for _, typ := range allowed {
			for _, b := range backends {
				if b.Type == typ {
					return &handle{Backend: b, pass: passphrase}, nil
				}
			}
		}
		return nil, keyring.ErrNoAvailImpl
	}
}

// handle is an opened backend. It carries the passphrase prompt it was opened with.
type handle struct {
	*Backend
	pass keyring.PromptFunc
}

func (h *handle) Get(key string) (keyring.Item, error) {
	v, ok := h.Items[key]
	if !ok {
		return keyring.Item{}, h.Missing
	}
	if h.NeedsPassphrase {
		if _, err := h.pass("Enter passphrase"); err != nil {
			return keyring.Item{}, err
		}
	}
	return keyring.Item{Key: key, Data: v}, nil
}

func (h *handle) Set(item keyring.Item) error {
	if h.NeedsPassphrase {
		if _, err := h.pass("Enter passphrase"); err != nil {
			return err
		}
	}
	h.Items[item.Key] = item.Data
	return nil
}

func (b *Backend) GetMetadata(key string) (keyring.Metadata, error) {
	if b.MetadataErr != nil {
		return keyring.Metadata{}, b.MetadataErr
	}
	if b.NoMetadata {
		return keyring.Metadata{}, keyring.ErrMetadataNotSupported
	}
	if _, ok := b.Items[key]; !ok {
		return keyring.Metadata{}, b.Missing
	}
	return keyring.Metadata{Item: &keyring.Item{Key: key}}, nil
}

func (b *Backend) Remove(key string) error {
	if b.RemoveErr != nil {
		return b.RemoveErr
	}
	if _, ok := b.Items[key]; !ok {
		return b.Missing
	}
	delete(b.Items, key)
	return nil
}

func (b *Backend) Keys() ([]string, error) {
	keys := make([]string, 0, len(b.Items))
	for k := range b.Items {
		keys = append(keys, k)
	}
	return keys, nil
}
