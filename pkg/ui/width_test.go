package ui

import "testing"

func TestWidthIgnoresColour(t *testing.T) {
	coloured := "\x1b[31m1.4GB\x1b[0m"
	if got, want := Width(coloured), 5; got != want {
		t.Errorf("Width(%q) = %d, want %d", coloured, got, want)
	}

	if got, want := Width("1.4GB"), 5; got != want {
		t.Errorf("Width() = %d, want %d", got, want)
	}
}

func TestPad(t *testing.T) {
	coloured := "\x1b[31m1.4GB\x1b[0m"

	if got, want := PadLeft(coloured, 8), "   "+coloured; got != want {
		t.Errorf("PadLeft() = %q, want %q", got, want)
	}

	if got, want := PadRight("go caches", 12), "go caches   "; got != want {
		t.Errorf("PadRight() = %q, want %q", got, want)
	}

	// Anything already at or over the width is left alone.
	if got, want := PadLeft("1.4GB", 3), "1.4GB"; got != want {
		t.Errorf("PadLeft() = %q, want %q", got, want)
	}

	if got, want := PadRight("1.4GB", 5), "1.4GB"; got != want {
		t.Errorf("PadRight() = %q, want %q", got, want)
	}
}
