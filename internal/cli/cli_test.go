package cli

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/byteness/keyring"

	"pins/internal/keyringtest"
	"pins/internal/tokenstore"

	_ "modernc.org/sqlite"
)

const apiPosts = `[
  {"href":"https://example.com/","description":"An Example","extended":"example description","meta":"m1","hash":"h1","time":"2019-11-06T22:50:31Z","shared":"yes","toread":"no","tags":"fish sheep goats"},
  {"href":"https://example.com/2","description":"Example 2","extended":"","meta":"m2","hash":"h2","time":"2018-11-06T22:50:31Z","shared":"no","toread":"yes","tags":"elephants goats lizards"}
]`

// fakePinboard records the fromdt of each request and serves apiPosts.
type fakePinboard struct {
	mu      sync.Mutex
	fromdts []string
	srv     *httptest.Server
}

func newFakePinboard(t *testing.T) *fakePinboard {
	t.Helper()
	f := &fakePinboard{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fromdts = append(f.fromdts, r.URL.Query().Get("fromdt"))
		f.mu.Unlock()
		if r.URL.Query().Get("auth_token") != "user:SECRET" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(apiPosts))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePinboard) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fromdts...)
}

type result struct {
	code           int
	stdout, stderr string
}

// harness runs the CLI against a fake Pinboard and fake keyring backends. The
// backends are given in the order the keyring tries them.
type harness struct {
	pinboard *fakePinboard
	dir      string // KeyringDir, which exists
	backends []*keyringtest.Backend
}

func newHarness(t *testing.T, backends ...*keyringtest.Backend) *harness {
	t.Helper()
	return &harness{pinboard: newFakePinboard(t), dir: t.TempDir(), backends: backends}
}

func (h *harness) run(stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	app := &App{
		BaseURL:     h.pinboard.srv.URL,
		Version:     "test",
		Stdin:       strings.NewReader(stdin),
		Stdout:      &out,
		Stderr:      &errOut,
		OpenKeyring: keyringtest.Opener(h.backends...),
		KeyringDir:  h.dir,
	}
	code := app.Run(context.Background(), args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

// keychain returns an empty fake macOS Keychain.
func keychain() *keyringtest.Backend { return keyringtest.NewBackend(keyring.KeychainBackend) }

// fileBackend returns an empty fake encrypted file. It needs a passphrase, and a
// missing item is reported as os.ErrNotExist.
func fileBackend() *keyringtest.Backend {
	b := keyringtest.NewBackend(keyring.FileBackend)
	b.Missing = os.ErrNotExist
	b.NeedsPassphrase = true
	return b
}

// storeToken puts token into b under the key pins uses.
func storeToken(b *keyringtest.Backend, token string) {
	b.Items[tokenstore.ItemKey] = []byte(token)
}

func TestAuthSetStoresTokenInKeychain(t *testing.T) {
	kc := keychain()
	h := newHarness(t, kc, fileBackend())

	r := h.run("user:SECRET\n", "auth", "set")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := string(kc.Items[tokenstore.ItemKey]); got != "user:SECRET" {
		t.Errorf("keychain holds %q", got)
	}
	if !strings.Contains(r.stdout, "Saved Pinboard token to macOS Keychain.") {
		t.Errorf("stdout = %q", r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, "SECRET") {
		t.Error("output echoes the token")
	}
}

func TestAuthSetRemovesStaleCopyFromOtherBackend(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	storeToken(file, "user:OLD")
	h := newHarness(t, kc, file)

	if r := h.run("user:NEW\n", "auth", "set"); r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if got := string(kc.Items[tokenstore.ItemKey]); got != "user:NEW" {
		t.Errorf("keychain holds %q", got)
	}
	if _, ok := file.Items[tokenstore.ItemKey]; ok {
		t.Error("stale copy left in the file backend")
	}
}

func TestAuthSetRejectsInvalidToken(t *testing.T) {
	for name, stdin := range map[string]string{
		"no colon":        "NOCOLONHERE\n",
		"empty username":  ":SECRET\n",
		"empty token":     "user:\n",
		"empty input":     "",
		"only whitespace": "   \n",
	} {
		t.Run(name, func(t *testing.T) {
			kc := keychain()
			h := newHarness(t, kc)
			r := h.run(stdin, "auth", "set")
			if r.code != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", r.code, r.stderr)
			}
			if _, ok := kc.Items[tokenstore.ItemKey]; ok {
				t.Error("invalid token was stored")
			}
		})
	}
}

func TestAuthSetFileBackendNeedsTerminal(t *testing.T) {
	file := fileBackend()
	h := newHarness(t, file)

	r := h.run("user:SECRET\n", "auth", "set")
	if r.code != 1 || !strings.Contains(r.stderr, "not a terminal") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if len(file.Items) != 0 {
		t.Error("token written without a passphrase")
	}
}

func TestAuthStatusReportsBackendWithoutPrintingToken(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)

	r := h.run("", "auth", "status")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if r.stdout != "Pinboard token is stored in macOS Keychain.\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
	if strings.Contains(r.stdout+r.stderr, "SECRET") {
		t.Error("status prints the token")
	}
}

func TestAuthStatusNothingStored(t *testing.T) {
	h := newHarness(t, keychain(), fileBackend())

	r := h.run("", "auth", "status")
	if r.code != 1 {
		t.Fatalf("exit %d, want 1", r.code)
	}
	if r.stdout != "" || !strings.Contains(r.stderr, "no Pinboard token is stored") {
		t.Errorf("stdout %q, stderr %q", r.stdout, r.stderr)
	}
}

func TestAuthStatusNeedsNoFilePassphrase(t *testing.T) {
	// The file backend's prompt fails on a non-terminal, so passing shows status never asked for it.
	file := fileBackend()
	storeToken(file, "user:SECRET")
	h := newHarness(t, file)

	r := h.run("", "auth", "status")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "encrypted file") {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestAuthUnsetRemovesFromEveryBackend(t *testing.T) {
	kc := keychain()
	file := fileBackend()
	storeToken(kc, "user:SECRET")
	storeToken(file, "user:SECRET")
	h := newHarness(t, kc, file)

	r := h.run("", "auth", "unset")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if len(kc.Items) != 0 || len(file.Items) != 0 {
		t.Errorf("items left behind: keychain %d, file %d", len(kc.Items), len(file.Items))
	}
	if !strings.Contains(r.stdout, "Removed Pinboard token from macOS Keychain.") ||
		!strings.Contains(r.stdout, "Removed Pinboard token from encrypted file") {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestAuthUnsetNothingStoredExitsZero(t *testing.T) {
	h := newHarness(t, keychain(), fileBackend())

	r := h.run("", "auth", "unset")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if r.stdout != "No Pinboard token was stored.\n" {
		t.Errorf("stdout = %q", r.stdout)
	}
}

func TestAuthUnsetReportsFailureAndStillTriesOtherBackends(t *testing.T) {
	kc := keychain()
	kc.RemoveErr = errTest("keychain locked")
	file := fileBackend()
	storeToken(kc, "user:SECRET")
	storeToken(file, "user:SECRET")
	h := newHarness(t, kc, file)

	r := h.run("", "auth", "unset")
	if r.code != 1 || !strings.Contains(r.stderr, "keychain locked") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if _, ok := file.Items[tokenstore.ItemKey]; ok {
		t.Error("file backend not cleared after the keychain failed")
	}
}

func TestAuthUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"no subcommand":      {"auth"},
		"unknown subcommand": {"auth", "bogus"},
		"old -a flag":        {"auth", "-a", "auth.json"},
		"status with args":   {"auth", "status", "extra"},
		"set with args":      {"auth", "set", "extra"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			kc := keychain()
			h := newHarness(t, kc)
			r := h.run("user:SECRET\n", args...)
			if r.code != 2 {
				t.Fatalf("exit %d, want 2 (stderr %q)", r.code, r.stderr)
			}
			if _, ok := kc.Items[tokenstore.ItemKey]; ok {
				t.Error("usage error still stored a token")
			}
		})
	}
}

func TestAuthNoSubcommandPrintsUsage(t *testing.T) {
	r := newHarness(t, keychain()).run("", "auth")
	if !strings.Contains(r.stderr, "Usage:") || !strings.Contains(r.stderr, "pins auth status") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestAuthHelpExitsZero(t *testing.T) {
	r := newHarness(t, keychain()).run("", "auth", "-h")
	if r.code != 0 || !strings.Contains(r.stdout, "pins auth unset") {
		t.Errorf("exit %d, stdout %q", r.code, r.stdout)
	}
}

func TestImportReadsTokenFromKeyring(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)
	dbPath := filepath.Join(t.TempDir(), "pinboard.db")

	r := h.run("", "import", dbPath)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "saved 2 posts") {
		t.Errorf("stderr = %q", r.stderr)
	}
}

func TestImportWithoutStoredTokenFailsBeforeCreatingDatabase(t *testing.T) {
	t.Chdir(t.TempDir())
	// An auth.json with a valid token must be ignored.
	if err := os.WriteFile("auth.json", []byte(`{"pinboard_token":"user:SECRET"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, keychain(), fileBackend())
	dbPath := filepath.Join(t.TempDir(), "pinboard.db")

	r := h.run("", "import", dbPath)
	if r.code != 1 || !strings.Contains(r.stderr, "run `pins auth set` first") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Error("database created although no token was found")
	}
	if len(h.pinboard.calls()) != 0 {
		t.Error("API called without a token")
	}
	if _, err := os.Stat("auth.json"); err != nil {
		t.Error("auth.json was touched")
	}
}

func TestImportFullFetchThenSince(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)
	dbPath := filepath.Join(t.TempDir(), "pinboard.db")

	// First run: no database yet, so --since fetches everything.
	r := h.run("", "import", dbPath, "--since")
	if r.code != 0 {
		t.Fatalf("first run exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "saved 2 posts") {
		t.Errorf("stderr = %q", r.stderr)
	}

	// Second run: fetch from the newest stored post.
	r = h.run("", "import", dbPath, "--since")
	if r.code != 0 {
		t.Fatalf("second run exit %d: %s", r.code, r.stderr)
	}

	calls := h.pinboard.calls()
	if len(calls) != 2 || calls[0] != "" || calls[1] != "2019-11-06T22:50:31Z" {
		t.Fatalf("fromdt calls = %q", calls)
	}
}

func TestImportSinceDate(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)

	r := h.run("", "import", filepath.Join(t.TempDir(), "p.db"), "--since-date", "2019-01-02 03:04:05")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if calls := h.pinboard.calls(); len(calls) != 1 || calls[0] != "2019-01-02T03:04:05Z" {
		t.Fatalf("fromdt calls = %q", calls)
	}
}

func TestImportRerunDoesNotDuplicate(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)
	dbPath := filepath.Join(t.TempDir(), "p.db")

	// The fake server ignores fromdt and always returns both posts, so re-runs
	// must update in place rather than insert again.
	for _, args := range [][]string{
		{"import", dbPath},
		{"import", dbPath},
		{"import", dbPath, "--since"},
	} {
		if r := h.run("", args...); r.code != 0 {
			t.Fatalf("%v exit %d: %s", args, r.code, r.stderr)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("rows = %d, want 2 after three runs", n)
	}
}

func TestImportUsageErrors(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:SECRET")
	h := newHarness(t, kc)
	db := filepath.Join(t.TempDir(), "p.db")

	tests := map[string][]string{
		"both since flags": {"import", db, "--since", "--since-date", "2019-01-01"},
		"no database":      {"import"},
		"bad since-date":   {"import", db, "--since-date", "not a date"},
		"old -a flag":      {"import", db, "-a", "auth.json"},
	}
	for name, args := range tests {
		if r := h.run("", args...); r.code != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr: %s)", name, r.code, r.stderr)
		}
	}
	if len(h.pinboard.calls()) != 0 {
		t.Errorf("API called on usage error: %q", h.pinboard.calls())
	}
}

func TestImportAPIRejectedTokenDoesNotLeakIt(t *testing.T) {
	kc := keychain()
	storeToken(kc, "user:WRONG")
	h := newHarness(t, kc)
	dbPath := filepath.Join(t.TempDir(), "p.db")

	r := h.run("", "import", dbPath)
	if r.code != 1 || !strings.Contains(r.stderr, "HTTP 401") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if strings.Contains(r.stdout+r.stderr, "WRONG") {
		t.Errorf("output leaks the token: stdout %q, stderr %q", r.stdout, r.stderr)
	}

	// The posts table is created only once the API has answered, so it may be
	// absent. Either way there must be no posts.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'posts'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables == 0 {
		return
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("rows = %d after a rejected token, want 0", n)
	}
}

func TestVersionAndUnknownCommand(t *testing.T) {
	h := newHarness(t, keychain())
	if r := h.run("", "version"); r.code != 0 || strings.TrimSpace(r.stdout) != "test" {
		t.Errorf("version: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := h.run("", "bogus"); r.code != 2 {
		t.Errorf("unknown command: exit %d, want 2", r.code)
	}
}

// errTest is a fixed error for injecting backend failures.
type errTest string

func (e errTest) Error() string { return string(e) }
