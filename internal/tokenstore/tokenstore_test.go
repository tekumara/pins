package tokenstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/byteness/keyring"

	"github.com/tekumara/pins/internal/keyringtest"
)

// These tests use only in-memory fakes, apart from TestFileBackendRoundTrip,
// which is limited to the file backend in a temp directory. No test touches a
// real Keychain, Credential Manager or Secret Service.

func keychain() *keyringtest.Backend { return keyringtest.NewBackend(keyring.KeychainBackend) }

func fileBackend() *keyringtest.Backend {
	b := keyringtest.NewBackend(keyring.FileBackend)
	b.Missing = os.ErrNotExist
	b.NeedsPassphrase = true
	return b
}

func storeToken(b *keyringtest.Backend, token string) {
	b.Items[ItemKey] = []byte(token)
}

// fixedPrompt answers each passphrase question with the next of answers, and counts the questions.
type fixedPrompt struct {
	answers []string
	asked   int
}

func (p *fixedPrompt) prompt(string) (string, error) {
	p.asked++
	if len(p.answers) == 0 {
		return "", errors.New("unexpected passphrase question")
	}
	a := p.answers[0]
	p.answers = p.answers[1:]
	return a, nil
}

// newStore returns a Store over the fakes. dir is the file backend's directory.
func newStore(dir string, prompt keyring.PromptFunc, backends ...*keyringtest.Backend) *Store {
	return New(keyringtest.Opener(backends...), dir, prompt)
}

func TestSetWritesToFirstBackendThatOpens(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, file)

	// Keychain comes first in Backends, so it is the active backend.
	typ, err := s.Set("user:SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if typ != keyring.KeychainBackend {
		t.Errorf("wrote to %s", typ)
	}
	if string(kc.Items[ItemKey]) != "user:SECRET" {
		t.Errorf("keychain holds %q", kc.Items[ItemKey])
	}
	if len(file.Items) != 0 {
		t.Error("file backend written while keychain was active")
	}
}

func TestSetFallsBackToFileWhenKeychainUnavailable(t *testing.T) {
	file := fileBackend()
	p := &fixedPrompt{answers: []string{"pw", "pw"}}
	s := newStore(t.TempDir(), p.prompt, file)

	typ, err := s.Set("user:SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if typ != keyring.FileBackend {
		t.Errorf("wrote to %s", typ)
	}
	if p.asked != 2 {
		t.Errorf("passphrase asked %d times, want 2 (enter and confirm)", p.asked)
	}
}

func TestSetRejectsMismatchedPassphrase(t *testing.T) {
	file := fileBackend()
	p := &fixedPrompt{answers: []string{"one", "two"}}
	s := newStore(t.TempDir(), p.prompt, file)

	if _, err := s.Set("user:SECRET"); !errors.Is(err, ErrPassphraseMismatch) {
		t.Fatalf("err = %v, want ErrPassphraseMismatch", err)
	}
	if len(file.Items) != 0 {
		t.Error("token written despite mismatched passphrase")
	}
}

func TestEmptyPassphraseRejected(t *testing.T) {
	file := fileBackend()
	p := &fixedPrompt{answers: []string{""}}
	s := newStore(t.TempDir(), p.prompt, file)

	if _, err := s.Set("user:SECRET"); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("err = %v, want ErrEmptyPassphrase", err)
	}
	if len(file.Items) != 0 {
		t.Error("token written with an empty passphrase")
	}
}

func TestSetRemovesStaleCopiesFromOtherBackends(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	storeToken(file, "user:OLD")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, file)

	if _, err := s.Set("user:NEW"); err != nil {
		t.Fatal(err)
	}
	if string(kc.Items[ItemKey]) != "user:NEW" {
		t.Errorf("keychain holds %q", kc.Items[ItemKey])
	}
	if _, ok := file.Items[ItemKey]; ok {
		t.Error("stale file copy left behind")
	}
}

func TestSetReportsStaleCopyDeleteFailureAfterWriting(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	storeToken(file, "user:OLD")
	file.RemoveErr = errors.New("file locked")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, file)

	typ, err := s.Set("user:NEW")
	if err == nil || !strings.Contains(err.Error(), "token saved to macOS Keychain") ||
		!strings.Contains(err.Error(), "file locked") {
		t.Fatalf("err = %v", err)
	}
	if typ != keyring.KeychainBackend {
		t.Errorf("typ = %s, want the backend holding the new token", typ)
	}
	if string(kc.Items[ItemKey]) != "user:NEW" {
		t.Error("new token not stored")
	}
}

func TestGetReturnsTokenFromActiveBackend(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc)

	got, err := s.Get()
	if err != nil || got != "user:SECRET" {
		t.Fatalf("Get = %q, %v", got, err)
	}
}

func TestGetNotStored(t *testing.T) {
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, keychain(), fileBackend())
	if _, err := s.Get(); !errors.Is(err, ErrNotStored) {
		t.Fatalf("err = %v, want ErrNotStored", err)
	}
}

func TestGetDoesNotCreateMissingFileDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keyring")
	s := newStore(dir, (&fixedPrompt{}).prompt, fileBackend())

	if _, err := s.Get(); !errors.Is(err, ErrNotStored) {
		t.Fatalf("err = %v, want ErrNotStored", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("Get created the file backend directory")
	}
}

func TestStatusReportsFirstHoldingBackend(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, fileBackend())

	typ, user, err := s.Status()
	if err != nil || typ != keyring.KeychainBackend || user != "user" {
		t.Fatalf("Status = %s, %q, %v", typ, user, err)
	}
}

func TestStatusFallsBackToGetWhenMetadataUnsupported(t *testing.T) {
	cred := keyringtest.NewBackend(keyring.WinCredBackend)
	cred.NoMetadata = true
	storeToken(cred, "user:SECRET")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, cred)

	typ, user, err := s.Status()
	if err != nil || typ != keyring.WinCredBackend || user != "user" {
		t.Fatalf("Status = %s, %q, %v", typ, user, err)
	}
}

func TestStatusReadsUsernameFromEncryptedFile(t *testing.T) {
	file := fileBackend()
	storeToken(file, "user:SECRET")
	p := &fixedPrompt{answers: []string{"pw"}}
	s := newStore(t.TempDir(), p.prompt, file)

	typ, user, err := s.Status()
	if err != nil || typ != keyring.FileBackend || user != "user" {
		t.Fatalf("Status = %s, %q, %v", typ, user, err)
	}
	if p.asked != 1 {
		t.Errorf("Status asked for a passphrase %d times, want 1", p.asked)
	}
}

func TestStatusNotStored(t *testing.T) {
	p := &fixedPrompt{}
	s := newStore(t.TempDir(), p.prompt, keychain(), fileBackend())
	if _, _, err := s.Status(); !errors.Is(err, ErrNotStored) {
		t.Fatalf("err = %v, want ErrNotStored", err)
	}
	if p.asked != 0 {
		t.Errorf("Status asked for a passphrase without a token: %d times", p.asked)
	}
}

func TestStatusFindsTokenPastFailingBackend(t *testing.T) {
	kc := keychain()
	kc.MetadataErr = errors.New("keychain check failed")
	file := fileBackend()
	storeToken(file, "user:SECRET")
	p := &fixedPrompt{answers: []string{"pw"}}
	s := newStore(t.TempDir(), p.prompt, kc, file)

	typ, user, err := s.Status()
	if err != nil || typ != keyring.FileBackend || user != "user" {
		t.Fatalf("Status = %s, %q, %v", typ, user, err)
	}
}

func TestStatusReportsCheckFailureWhenNothingHeld(t *testing.T) {
	kc := keychain()
	kc.MetadataErr = errors.New("keychain check failed")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc)

	_, _, err := s.Status()
	if err == nil || errors.Is(err, ErrNotStored) || !strings.Contains(err.Error(), "keychain check failed") {
		t.Fatalf("err = %v, want the check failure, not ErrNotStored", err)
	}
}

func TestUnsetRemovesFromEveryBackend(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	storeToken(kc, "user:SECRET")
	storeToken(file, "user:SECRET")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, file)

	removed, err := s.Unset()
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != keyring.KeychainBackend || removed[1] != keyring.FileBackend {
		t.Errorf("removed = %v", removed)
	}
	if len(kc.Items) != 0 || len(file.Items) != 0 {
		t.Error("items left behind")
	}
}

func TestUnsetNothingStoredIsNotAnError(t *testing.T) {
	// Both "missing" signals: the keyring's sentinel and the file backend's os.ErrNotExist.
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, keychain(), fileBackend())
	removed, err := s.Unset()
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
}

func TestUnsetToleratesItemVanishingBeforeRemove(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	kc.RemoveErr = keyring.ErrKeyNotFound
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc)

	removed, err := s.Unset()
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
}

func TestUnsetTriesEveryBackendAfterFailure(t *testing.T) {
	kc := keychain()
	kc.RemoveErr = errors.New("keychain locked")
	file := fileBackend()
	storeToken(kc, "user:SECRET")
	storeToken(file, "user:SECRET")
	s := newStore(t.TempDir(), (&fixedPrompt{}).prompt, kc, file)

	removed, err := s.Unset()
	if err == nil || !strings.Contains(err.Error(), "keychain locked") {
		t.Fatalf("err = %v", err)
	}
	if len(removed) != 1 || removed[0] != keyring.FileBackend {
		t.Errorf("removed = %v, want only the file backend", removed)
	}
	if _, ok := file.Items[ItemKey]; ok {
		t.Error("file backend not cleared after keychain failure")
	}
}

func TestUnsetSkipsFileBackendWithoutDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keyring")
	s := newStore(dir, (&fixedPrompt{}).prompt, fileBackend())

	if removed, err := s.Unset(); err != nil || len(removed) != 0 {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Error("Unset created the file backend directory")
	}
}

func TestPromptRefusesNonTerminalWithoutWriting(t *testing.T) {
	var out strings.Builder
	prompt := Prompt(strings.NewReader("ignored\n"), &out)

	if _, err := prompt("Enter passphrase"); !errors.Is(err, ErrNotTerminal) {
		t.Fatalf("err = %v, want ErrNotTerminal", err)
	}
	if out.Len() != 0 {
		t.Errorf("prompt wrote %q without a terminal", out.String())
	}
}

// TestFileBackendRoundTrip uses the real keyring library, restricted to the
// file backend in a temp directory. It never reaches the OS keyrings.
func TestFileBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	open := OSOpener(dir)
	pass := func(string) (string, error) { return "correct horse", nil }

	s := New(open, dir, pass)
	s.backends = []keyring.BackendType{keyring.FileBackend}

	if _, err := s.Set("user:SECRET"); err != nil {
		t.Fatal(err)
	}

	// A fresh Store reads what the first one wrote.
	s2 := New(open, dir, pass)
	s2.backends = []keyring.BackendType{keyring.FileBackend}
	got, err := s2.Get()
	if err != nil || got != "user:SECRET" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if typ, user, err := s2.Status(); err != nil || typ != keyring.FileBackend || user != "user" {
		t.Fatalf("Status = %s, %q, %v", typ, user, err)
	}

	// A wrong passphrase must not return the token.
	wrong := New(open, dir, func(string) (string, error) { return "wrong", nil })
	wrong.backends = []keyring.BackendType{keyring.FileBackend}
	if got, err := wrong.Get(); err == nil {
		t.Errorf("Get with wrong passphrase returned %q", got)
	}

	removed, err := s2.Unset()
	if err != nil || len(removed) != 1 {
		t.Fatalf("Unset = %v, %v", removed, err)
	}
	if _, _, err := s2.Status(); !errors.Is(err, ErrNotStored) {
		t.Errorf("after unset, Status err = %v", err)
	}
}
