// Command pins saves your Pinboard bookmarks to a SQLite database.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"pins/internal/cli"
	"pins/internal/pinboard"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &cli.App{
		BaseURL: pinboard.DefaultBaseURL,
		Version: version,
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	}
	code := app.Run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}
