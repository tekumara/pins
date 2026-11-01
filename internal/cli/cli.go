// Package cli implements the pins command line.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"pins/internal/pinboard"
	"pins/internal/store"
)

const defaultAuthPath = "auth.json"

// App holds the process-level dependencies, so tests can substitute them.
type App struct {
	BaseURL string // Pinboard API root; tests point this at httptest.
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
}

// errUsage marks errors caused by bad arguments, which exit with status 2.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

// Run executes args (without the program name) and returns the exit status.
func (a *App) Run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		a.usage(a.Stderr)
		return 2
	}

	var err error
	switch args[0] {
	case "auth":
		err = a.runAuth(args[1:])
	case "import":
		err = a.runPosts(ctx, args[1:])
	case "version", "--version":
		fmt.Fprintln(a.Stdout, a.Version)
	case "help", "-h", "--help":
		a.usage(a.Stdout)
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n\n", args[0])
		a.usage(a.Stderr)
		return 2
	}

	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case isUsage(err):
		fmt.Fprintln(a.Stderr, "error:", err)
		return 2
	default:
		fmt.Fprintln(a.Stderr, "error:", err)
		return 1
	}
}

func isUsage(err error) bool {
	var u errUsage
	return errors.As(err, &u)
}

func (a *App) usage(w io.Writer) {
	fmt.Fprint(w, `Save data from Pinboard to a SQLite database.

Usage:
  pins auth [-a AUTH]
      Prompt for your Pinboard API token and save it to AUTH (default auth.json).
  pins import DATABASE [-a AUTH] [--since | --since-date DATE]
      Import posts into DATABASE. Re-running updates existing posts.
  pins version

Options for import:
  --since             fetch posts newer than the latest post already in DATABASE
  --since-date DATE   fetch posts created after DATE (YYYY-MM-DD or RFC 3339; UTC if no zone)
`)
}

// newFlags returns a FlagSet that reports errors without exiting or printing usage itself.
func newFlags(name string, w io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(w)
	return fs
}

// authFlag registers -a/--auth on fs.
func authFlag(fs *flag.FlagSet, help string) *string {
	var p string
	fs.StringVar(&p, "a", defaultAuthPath, help)
	fs.StringVar(&p, "auth", defaultAuthPath, help)
	return &p
}

func (a *App) runAuth(args []string) error {
	fs := newFlags("auth", a.Stderr)
	authPath := authFlag(fs, "path to save token to")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return usageOrHelp(err)
	}
	if len(positional) != 0 {
		return errUsage{"auth takes no positional arguments"}
	}

	fmt.Fprintln(a.Stdout, "Find your API token here: https://pinboard.in/settings/password")
	fmt.Fprintln(a.Stdout, "Paste the whole thing including your username (e.g. yourname:xxxyyyzzz) below.")
	fmt.Fprintln(a.Stdout)
	fmt.Fprint(a.Stdout, "API Token: ")

	line, err := bufio.NewReader(a.Stdin).ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return fmt.Errorf("reading token: %w", err)
	}
	token := strings.TrimSpace(line)
	if !strings.Contains(token, ":") {
		return errUsage{"token must look like username:TOKEN"}
	}

	if err := saveToken(*authPath, token); err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Saved token to %s\n", *authPath)
	return nil
}

// saveToken writes pinboard_token into path, keeping other keys, with owner-only permissions.
func saveToken(path, token string) error {
	data := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &data); err != nil {
			return fmt.Errorf("existing auth file %s is not valid JSON: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	data["pinboard_token"] = token

	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	// OpenFile applies the mode only on create, so tighten an existing file too.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(out, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readToken loads pinboard_token from the auth file at path.
func readToken(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("auth file %s not found; run `pins auth` first", path)
	}
	if err != nil {
		return "", err
	}
	var data struct {
		Token string `json:"pinboard_token"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", fmt.Errorf("auth file %s is not valid JSON: %w", path, err)
	}
	if data.Token == "" {
		return "", fmt.Errorf("auth file %s has no pinboard_token", path)
	}
	return data.Token, nil
}

func (a *App) runPosts(ctx context.Context, args []string) error {
	fs := newFlags("import", a.Stderr)
	authPath := authFlag(fs, "path to read auth token from")
	since := fs.Bool("since", false, "fetch posts newer than the latest post in the database")
	sinceDate := fs.String("since-date", "", "fetch posts created after DATE")
	positional, err := parseInterleaved(fs, args)
	if err != nil {
		return usageOrHelp(err)
	}
	if len(positional) != 1 {
		return errUsage{"import requires exactly one DATABASE argument"}
	}
	if *since && *sinceDate != "" {
		return errUsage{"use either --since or --since-date, not both"}
	}
	dbPath := positional[0]

	token, err := readToken(*authPath)
	if err != nil {
		return err
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	var fromdt string
	switch {
	case *since:
		fromdt, err = store.LatestTime(ctx, db)
		if err != nil {
			return err
		}
		if fromdt == "" {
			fmt.Fprintln(a.Stderr, "no existing posts in database; fetching all posts")
		}
	case *sinceDate != "":
		fromdt, err = pinboard.FormatFromDT(*sinceDate)
		if err != nil {
			return errUsage{err.Error()}
		}
	}

	client := pinboard.NewClient(token)
	client.BaseURL = a.BaseURL

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	w, err := store.NewWriter(ctx, tx)
	if err != nil {
		return err
	}
	defer w.Close()

	if err := client.AllPosts(ctx, fromdt, w.Upsert); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	fmt.Fprintf(a.Stderr, "saved %d posts to %s\n", w.Count(), dbPath)
	return nil
}

// parseInterleaved parses flags that may appear before, between or after positional
// arguments, as click does. It returns the positional arguments in order.
// Go's flag package stops at the first positional, so parsing resumes after each one.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

// usageOrHelp turns flag-parsing errors into usage errors, and passes -h through.
func usageOrHelp(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	return errUsage{err.Error()}
}
