package tokenstore

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/byteness/keyring"
	"golang.org/x/term"
)

var (
	// ErrNotTerminal means a passphrase is needed but stdin is not a terminal.
	ErrNotTerminal = errors.New("the encrypted token file needs a passphrase, but stdin is not a terminal")
	// ErrEmptyPassphrase means the passphrase was empty.
	ErrEmptyPassphrase = errors.New("passphrase must not be empty")
	// ErrPassphraseMismatch means the two passphrase entries differed.
	ErrPassphraseMismatch = errors.New("passphrases do not match")
)

// Prompt returns a passphrase prompt that reads from in, a terminal, and writes
// its question to errOut. It never writes to stdout, so it cannot mix with output.
// keyring.TerminalPrompt is not used because it writes to stdout.
func Prompt(in io.Reader, errOut io.Writer) keyring.PromptFunc {
	return func(msg string) (string, error) {
		return ReadHidden(in, errOut, msg+": ")
	}
}

// IsTerminal reports whether in is a terminal.
func IsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ReadHidden reads one line from in with echo off, writing prompt to out. It
// returns ErrNotTerminal, without reading, when in is not a terminal.
func ReadHidden(in io.Reader, out io.Writer, prompt string) (string, error) {
	if !IsTerminal(in) {
		return "", ErrNotTerminal
	}
	fd := int(in.(*os.File).Fd())
	fmt.Fprint(out, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return string(b), nil
}

// nonEmpty rejects an empty passphrase before the file backend sees it.
func nonEmpty(prompt keyring.PromptFunc) keyring.PromptFunc {
	return func(msg string) (string, error) {
		p, err := prompt(msg)
		if err != nil {
			return "", err
		}
		if p == "" {
			return "", ErrEmptyPassphrase
		}
		return p, nil
	}
}

// confirmed asks for the passphrase twice and requires the two entries to match.
// It is used when writing, so a typo cannot lock the token away.
func confirmed(prompt keyring.PromptFunc) keyring.PromptFunc {
	return func(msg string) (string, error) {
		first, err := prompt(msg)
		if err != nil {
			return "", err
		}
		second, err := prompt("Confirm passphrase")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", ErrPassphraseMismatch
		}
		return first, nil
	}
}
