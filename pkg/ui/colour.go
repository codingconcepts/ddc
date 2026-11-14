// Package ui holds the terminal presentation helpers shared by the config and
// runner packages, so that what a colour means is decided in one place.
package ui

import (
	"os"

	"github.com/fatih/color"
	"github.com/mattn/go-isatty"
)

var (
	// Size colours a formatted size, e.g. the "1.4GB" of "size=1.4GB".
	Size = color.New(color.FgRed).SprintFunc()

	// Name colours whatever is being acted on: a file or directory path, an
	// image tag, a volume name.
	Name = color.New(color.FgYellow).SprintFunc()

	// Dim colours incidental markers such as the "[dry-run]" log prefix.
	Dim = color.New(color.FgHiBlack).SprintFunc()
)

func init() {
	// Every log line goes to stderr, but fatih/color decides whether to emit
	// escape codes by looking at stdout. Re-test against stderr so that
	// piping stdout alone doesn't strip the colour, and respect NO_COLOR.
	if os.Getenv("NO_COLOR") != "" {
		color.NoColor = true
		return
	}

	if os.Getenv("CLICOLOR_FORCE") != "" {
		color.NoColor = false
		return
	}

	fd := os.Stderr.Fd()
	color.NoColor = !isatty.IsTerminal(fd) && !isatty.IsCygwinTerminal(fd)
}
