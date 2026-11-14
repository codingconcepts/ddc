package ui

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestHandlerLeavesValuesUnescaped(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(NewHandler(&buf, slog.LevelInfo, "[dry-run] "))

	// The whole point of the handler: a coloured value keeps its escape
	// codes, where slog's TextHandler would print them as "\x1b[31m".
	log.Info("deleting file", "path", "\x1b[33m/tmp/a.log\x1b[0m", "size", "\x1b[31m2.0KB\x1b[0m")

	got := buf.String()

	want := "[dry-run] INFO deleting file path=\x1b[33m/tmp/a.log\x1b[0m size=\x1b[31m2.0KB\x1b[0m\n"
	if got != want {
		t.Errorf("Handle() = %q, want %q", got, want)
	}
}

func TestHandlerQuotesSpacedValues(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(NewHandler(&buf, slog.LevelInfo, ""))

	log.Info("docker disk usage", "type", "Local Volumes", "count", 221)

	got := strings.TrimSpace(buf.String())

	want := `INFO docker disk usage type="Local Volumes" count=221`
	if got != want {
		t.Errorf("Handle() = %q, want %q", got, want)
	}
}

func TestHandlerLevel(t *testing.T) {
	var buf bytes.Buffer

	h := NewHandler(&buf, slog.LevelInfo, "")

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should be disabled at info level")
	}

	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("warn should be enabled at info level")
	}

	slog.New(h).Debug("quiet")

	if buf.Len() != 0 {
		t.Errorf("debug record written at info level: %q", buf.String())
	}
}

func TestHandlerWithAttrsAndGroup(t *testing.T) {
	var buf bytes.Buffer

	log := slog.New(NewHandler(&buf, slog.LevelInfo, "")).With("run", "1").WithGroup("docker")

	log.Info("pruning", "count", 2)

	got := strings.TrimSpace(buf.String())

	want := "INFO pruning run=1 docker.count=2"
	if got != want {
		t.Errorf("Handle() = %q, want %q", got, want)
	}
}
