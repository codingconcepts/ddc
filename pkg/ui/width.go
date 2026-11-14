package ui

import (
	"regexp"
	"strings"
)

// ansi matches the colour escape sequences fatih/color writes, which take up
// no space on screen but would otherwise be counted when aligning columns.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// Width reports how wide s is on screen, ignoring colour escape sequences.
func Width(s string) int {
	return len([]rune(ansi.ReplaceAllString(s, "")))
}

// PadLeft right-aligns s in a field of n columns, measuring s by its printable
// width so that a coloured value lines up with an uncoloured one.
func PadLeft(s string, n int) string {
	if pad := n - Width(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}

	return s
}

// PadRight left-aligns s in a field of n columns.
func PadRight(s string, n int) string {
	if pad := n - Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}

	return s
}
