package runner

import (
	"fmt"
	"io"
	"strings"

	"github.com/codingconcepts/ddc/pkg/config"
	"github.com/codingconcepts/ddc/pkg/ui"
)

// The steps a run is broken down into. One step can report several times (git
// gc and bazel clean run once per repository or workspace found), so the
// summary sums them rather than listing each occurrence.
const (
	stepDockerImages     = "docker images"
	stepDockerContainers = "docker containers"
	stepDockerVolumes    = "docker volumes"
	stepDockerBuildCache = "docker build cache"
	stepGoCaches         = "go caches"
	stepHomebrew         = "homebrew"
	stepBazel            = "bazel clean"
	stepGitGC            = "git gc"
	stepFilesystem       = "files and directories"
	stepPaths            = "explicit paths"
)

// summary accumulates what each step reclaimed so that the run can end with
// one table and a total, rather than leaving the reader to add up the lines
// scattered through the log.
type summary struct {
	order []string
	sizes map[string]int64
}

func newSummary() *summary {
	return &summary{sizes: map[string]int64{}}
}

// add records size against a step, summing repeat calls. Sizes that aren't
// positive are dropped, so a step that reclaimed nothing (or that couldn't be
// measured) stays out of the table instead of padding it with zeroes.
func (s *summary) add(step string, size int64) {
	if size <= 0 {
		return
	}

	if _, ok := s.sizes[step]; !ok {
		s.order = append(s.order, step)
	}

	s.sizes[step] += size
}

func (s *summary) total() int64 {
	var total int64
	for _, size := range s.sizes {
		total += size
	}

	return total
}

// print writes the per-step breakdown followed by the total. Steps are listed
// in the order they first reported, which is the order they ran in.
//
// It writes to w directly rather than through log or slog, both of which would
// decorate a table with a level and a prefix.
func (s *summary) print(w io.Writer, dryRun bool) {
	heading, totalLabel := "reclaimed", "total reclaimed"
	if dryRun {
		heading, totalLabel = "would reclaim", "total would reclaim"
	}

	if len(s.order) == 0 {
		fmt.Fprintf(w, "\nnothing to reclaim\n")
		return
	}

	// Width the two columns against every row, including the total, so the
	// sizes line up under one another.
	labelWidth := len(totalLabel)

	var sizeWidth int

	formatted := make(map[string]string, len(s.order))
	for _, step := range s.order {
		size := config.FormatSize(s.sizes[step])
		formatted[step] = size
		labelWidth = max(labelWidth, len(step))
		sizeWidth = max(sizeWidth, ui.Width(size))
	}

	total := config.FormatSize(s.total())
	sizeWidth = max(sizeWidth, ui.Width(total))

	fmt.Fprintf(w, "\n%s\n", heading)

	for _, step := range s.order {
		fmt.Fprintf(w, "  %s  %s\n", ui.PadRight(step, labelWidth), ui.PadLeft(formatted[step], sizeWidth))
	}

	fmt.Fprintf(w, "  %s\n", ui.Dim(strings.Repeat("-", labelWidth+2+sizeWidth)))
	fmt.Fprintf(w, "  %s  %s\n", ui.PadRight(totalLabel, labelWidth), ui.PadLeft(total, sizeWidth))
}
