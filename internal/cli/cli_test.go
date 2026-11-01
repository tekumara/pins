package cli

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

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

func (f *fakePinboard) app(stdin string) (*App, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &App{
		BaseURL: f.srv.URL,
		Version: "test",
		Stdin:   strings.NewReader(stdin),
		Stdout:  &out,
		Stderr:  &errOut,
	}, &out, &errOut
}

func run(t *testing.T, f *fakePinboard, stdin string, args ...string) result {
	t.Helper()
	app, out, errOut := f.app(stdin)
	code := app.Run(context.Background(), args)
	return result{code: code, stdout: out.String(), stderr: errOut.String()}
}

func writeAuth(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(`{"pinboard_token":"user:SECRET"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthSavesTokenWithOwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	// Existing file with a looser mode and an unrelated key, which should survive.
	if err := os.WriteFile(path, []byte(`{"other":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := run(t, newFakePinboard(t), "user:SECRET\n", "auth", "-a", path)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"pinboard_token": "user:SECRET"`) || !strings.Contains(string(raw), `"other": 1`) {
		t.Errorf("auth.json = %s", raw)
	}
}

func TestAuthRejectsTokenWithoutUsername(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	r := run(t, newFakePinboard(t), "NOCOLONHERE\n", "auth", "-a", path)
	if r.code != 2 {
		t.Fatalf("exit %d, want 2", r.code)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("auth file written for an invalid token")
	}
}

func TestPostsFullFetchThenSince(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	dbPath := filepath.Join(dir, "pinboard.db")
	writeAuth(t, auth)
	f := newFakePinboard(t)

	// First run: no database yet, so --since fetches everything.
	r := run(t, f, "", "posts", dbPath, "-a", auth, "--since")
	if r.code != 0 {
		t.Fatalf("first run exit %d: %s", r.code, r.stderr)
	}
	if !strings.Contains(r.stderr, "saved 2 posts") {
		t.Errorf("stderr = %q", r.stderr)
	}

	// Second run: fetch from the newest stored post.
	r = run(t, f, "", "posts", dbPath, "-a", auth, "--since")
	if r.code != 0 {
		t.Fatalf("second run exit %d: %s", r.code, r.stderr)
	}

	calls := f.calls()
	if len(calls) != 2 || calls[0] != "" || calls[1] != "2019-11-06T22:50:31Z" {
		t.Fatalf("fromdt calls = %q", calls)
	}
}

func TestPostsSinceDate(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	writeAuth(t, auth)
	f := newFakePinboard(t)

	r := run(t, f, "", "posts", filepath.Join(dir, "p.db"), "-a", auth, "--since-date", "2019-01-02 03:04:05")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if calls := f.calls(); len(calls) != 1 || calls[0] != "2019-01-02T03:04:05Z" {
		t.Fatalf("fromdt calls = %q", calls)
	}
}

func TestPostsRerunDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	dbPath := filepath.Join(dir, "p.db")
	writeAuth(t, auth)
	f := newFakePinboard(t)

	// The fake server ignores fromdt and always returns both posts, so re-runs
	// must update in place rather than insert again.
	for _, args := range [][]string{
		{"posts", dbPath, "-a", auth},
		{"posts", dbPath, "-a", auth},
		{"posts", dbPath, "-a", auth, "--since"},
	} {
		if r := run(t, f, "", args...); r.code != 0 {
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

func TestPostsUsageErrors(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	writeAuth(t, auth)
	f := newFakePinboard(t)
	db := filepath.Join(dir, "p.db")

	tests := map[string][]string{
		"both since flags": {"posts", db, "-a", auth, "--since", "--since-date", "2019-01-01"},
		"no database":      {"posts", "-a", auth},
		"bad since-date":   {"posts", db, "-a", auth, "--since-date", "not a date"},
	}
	for name, args := range tests {
		if r := run(t, f, "", args...); r.code != 2 {
			t.Errorf("%s: exit %d, want 2 (stderr: %s)", name, r.code, r.stderr)
		}
	}
	if len(f.calls()) != 0 {
		t.Errorf("API called on usage error: %q", f.calls())
	}
}

func TestPostsMissingAuthFile(t *testing.T) {
	dir := t.TempDir()
	r := run(t, newFakePinboard(t), "", "posts", filepath.Join(dir, "p.db"), "-a", filepath.Join(dir, "nope.json"))
	if r.code != 1 || !strings.Contains(r.stderr, "run `pins auth` first") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestPostsAPIRejectedTokenLeavesDatabaseUntouched(t *testing.T) {
	dir := t.TempDir()
	auth := filepath.Join(dir, "auth.json")
	dbPath := filepath.Join(dir, "p.db")
	if err := os.WriteFile(auth, []byte(`{"pinboard_token":"user:WRONG"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	r := run(t, newFakePinboard(t), "", "posts", dbPath, "-a", auth)
	if r.code != 1 || !strings.Contains(r.stderr, "HTTP 401") {
		t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
	}
	if strings.Contains(r.stderr, "WRONG") {
		t.Errorf("error output leaks the token: %q", r.stderr)
	}
}

func TestVersionAndUnknownCommand(t *testing.T) {
	f := newFakePinboard(t)
	if r := run(t, f, "", "version"); r.code != 0 || strings.TrimSpace(r.stdout) != "test" {
		t.Errorf("version: exit %d, stdout %q", r.code, r.stdout)
	}
	if r := run(t, f, "", "bogus"); r.code != 2 {
		t.Errorf("unknown command: exit %d, want 2", r.code)
	}
}
