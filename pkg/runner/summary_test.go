package runner

import (
	"bytes"
	"strings"
	"testing"
)

func TestSummaryAddSumsAndIgnoresNonPositive(t *testing.T) {
	s := newSummary()
	s.add(stepGitGC, 1_000)
	s.add(stepGitGC, 2_000)
	s.add(stepBazel, 0)
	s.add(stepFilesystem, -5_000)
	s.add(stepDockerImages, 4_000)

	if got, want := s.total(), int64(7_000); got != want {
		t.Errorf("total() = %d, want %d", got, want)
	}

	want := []string{stepGitGC, stepDockerImages}
	if len(s.order) != len(want) {
		t.Fatalf("order = %v, want %v", s.order, want)
	}

	for i, step := range want {
		if s.order[i] != step {
			t.Errorf("order[%d] = %q, want %q", i, s.order[i], step)
		}
	}
}

func TestSummaryPrint(t *testing.T) {
	s := newSummary()
	s.add(stepDockerImages, 12_400_000_000)
	s.add(stepFilesystem, 2_000_000)

	var buf bytes.Buffer
	s.print(&buf, false)
	out := buf.String()

	for _, want := range []string{"reclaimed", "docker images", "12.4GB", "files and directories", "2.0MB", "total reclaimed", "12.4GB"} {
		if !strings.Contains(out, want) {
			t.Errorf("print() output missing %q:\n%s", want, out)
		}
	}

	// The sizes share a right-hand edge, so every row ends at the same column.
	var width int

	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}

		if width == 0 {
			width = len(line)
			continue
		}

		if len(line) != width {
			t.Errorf("row %q is %d wide, want %d:\n%s", line, len(line), width, out)
		}
	}
}

func TestSummaryPrintDryRun(t *testing.T) {
	s := newSummary()
	s.add(stepPaths, 5_000_000)

	var buf bytes.Buffer
	s.print(&buf, true)
	out := buf.String()

	if !strings.Contains(out, "would reclaim") {
		t.Errorf("dry run output should say what would be reclaimed:\n%s", out)
	}

	if strings.Contains(out, "total reclaimed") {
		t.Errorf("dry run output should not claim anything was reclaimed:\n%s", out)
	}
}

func TestSummaryPrintEmpty(t *testing.T) {
	var buf bytes.Buffer
	newSummary().print(&buf, false)

	if got, want := strings.TrimSpace(buf.String()), "nothing to reclaim"; got != want {
		t.Errorf("print() = %q, want %q", got, want)
	}
}

func TestParseBrewFreed(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want int64
	}{
		{
			name: "freed",
			out:  "Removing: /Users/x/Library/Caches/Homebrew/go--1.25.0.bottle.tar.gz... (74.9MB)\n==> This operation has freed approximately 1.2GB of disk space.\n",
			want: 1_200_000_000,
		},
		{
			name: "dry run",
			out:  "==> This operation would free approximately 930.6MB of disk space.\n",
			want: 930_600_000,
		},
		{
			name: "kilobytes",
			out:  "This operation has freed approximately 12.3KB of disk space.",
			want: 12_300,
		},
		{
			name: "bytes",
			out:  "This operation has freed approximately 100B of disk space.",
			want: 100,
		},
		{"no size reported", "Pruned 0 symbolic links and 0 directories from /opt/homebrew\n", 0},
		{"empty", "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseBrewFreed(tt.out); got != tt.want {
				t.Errorf("parseBrewFreed() = %d, want %d", got, tt.want)
			}
		})
	}
}
