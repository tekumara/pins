// Package cli implements the pins command line.
package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"pins/internal/pinboard"
	"pins/internal/store"
	"pins/internal/tokenstore"
)

// App holds the process-level dependencies, so tests can substitute them.
type App struct {
	BaseURL string // Pinboard API root; tests point this at httptest.
	Version string
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer

	// OpenKeyring opens the keyring. Nil uses the OS keyring.
	OpenKeyring tokenstore.Opener
	// KeyringDir is the encrypted file backend's directory. Empty uses
	// tokenstore.DefaultDir.
	KeyringDir string
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
  pins auth status
      Show where your Pinboard API token is stored. Exits 1 if none is stored.
  pins auth set
      Store your Pinboard API token (username:TOKEN). Reads it from the terminal,
      or from one line on stdin when stdin is not a terminal.
  pins auth unset
      Delete the stored Pinboard API token from every keyring that holds it.
  pins import DATABASE [--since | --since-date DATE]
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

// store returns the token store for this App.
func (a *App) store() (*tokenstore.Store, error) {
	dir := a.KeyringDir
	if dir == "" {
		d, err := tokenstore.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	open := a.OpenKeyring
	if open == nil {
		open = tokenstore.OSOpener(dir)
	}
	return tokenstore.New(open, dir, tokenstore.Prompt(a.Stdin, a.Stderr)), nil
}

func (a *App) runAuth(args []string) error {
	if len(args) == 0 {
		a.usage(a.Stderr)
		return errUsage{"auth needs a subcommand: status, set or unset"}
	}
	sub, rest := args[0], args[1:]
	if sub == "-h" || sub == "--help" || sub == "help" {
		a.usage(a.Stdout)
		return nil
	}
	if len(rest) != 0 {
		return errUsage{fmt.Sprintf("auth %s takes no arguments", sub)}
	}

	switch sub {
	case "status":
		return a.authStatus()
	case "set":
		return a.authSet()
	case "unset":
		return a.authUnset()
	default:
		return errUsage{fmt.Sprintf("unknown auth subcommand %q; use status, set or unset", sub)}
	}
}

// authStatus reports which backend holds the token. It never prints the token.
func (a *App) authStatus() error {
	s, err := a.store()
	if err != nil {
		return err
	}
	typ, err := s.Status()
	if errors.Is(err, tokenstore.ErrNotStored) {
		return fmt.Errorf("%w; run `pins auth set` to store one", err)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Pinboard token is stored in %s.\n", s.Name(typ))
	return nil
}

// authSet stores a username:TOKEN pair in the keyring. The token comes from a hidden
// terminal prompt, or from one line on stdin when stdin is not a terminal.
func (a *App) authSet() error {
	var token string
	if tokenstore.IsTerminal(a.Stdin) {
		fmt.Fprintln(a.Stderr, "Find your API token at https://pinboard.in/settings/password")
		line, err := tokenstore.ReadHidden(a.Stdin, a.Stderr, "Paste it with your username (e.g. yourname:xxxyyyzzz): ")
		if err != nil {
			return err
		}
		token = line
	} else {
		// EOF is fine here: a last line without a newline, or no input at all,
		// both fail the validation below as a usage error.
		line, err := bufio.NewReader(a.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading token: %w", err)
		}
		token = line
	}
	token = strings.TrimSpace(token)
	if user, secret, ok := strings.Cut(token, ":"); !ok || user == "" || secret == "" {
		return errUsage{"token must look like username:TOKEN"}
	}

	s, err := a.store()
	if err != nil {
		return err
	}
	typ, err := s.Set(token)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Stdout, "Saved Pinboard token to %s.\n", s.Name(typ))
	return nil
}

// authUnset deletes the token from every backend that holds it. Nothing stored is not an error.
func (a *App) authUnset() error {
	s, err := a.store()
	if err != nil {
		return err
	}
	removed, err := s.Unset()
	for _, typ := range removed {
		fmt.Fprintf(a.Stdout, "Removed Pinboard token from %s.\n", s.Name(typ))
	}
	if err != nil {
		return err
	}
	if len(removed) == 0 {
		fmt.Fprintln(a.Stdout, "No Pinboard token was stored.")
	}
	return nil
}

func (a *App) runPosts(ctx context.Context, args []string) error {
	fs := newFlags("import", a.Stderr)
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

	// Read the token before opening the database, so a failure creates no file.
	s, err := a.store()
	if err != nil {
		return err
	}
	token, err := s.Get()
	if errors.Is(err, tokenstore.ErrNotStored) {
		return fmt.Errorf("%w; run `pins auth set` first", err)
	}
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
