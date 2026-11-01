package tokenstore

import (
	"os"
	"path/filepath"

	"github.com/byteness/keyring"
)

// OSOpener returns the Opener for the real keyring. It opens the OS keyring for
// the allowed backends, and keeps the encrypted file backend in fileDir.
func OSOpener(fileDir string) Opener {
	return func(allowed []keyring.BackendType, passphrase keyring.PromptFunc) (keyring.Keyring, error) {
		return keyring.Open(keyring.Config{
			ServiceName:     "pins",
			AllowedBackends: allowed,
			// A nil trusted list lets only the creating app read the item, so pins
			// can read its own token without a prompt. The default (false) trusts
			// nothing, and every read prompts.
			KeychainTrustApplication: true,
			FileDir:                  fileDir,
			FilePasswordFunc:         passphrase,
		})
	}
}

// DefaultDir returns the directory for the encrypted file backend:
// the user config directory, then pins/keyring.
func DefaultDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pins", "keyring"), nil
}
