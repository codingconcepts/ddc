package ui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
)

// Handler is a slog handler that writes `LEVEL msg key=value` lines without
// escaping its values. slog's own TextHandler quotes anything containing a
// control character, which turns a coloured value into a literal
// "\x1b[31m1.4GB\x1b[0m" on screen.
type Handler struct {
	mu     *sync.Mutex
	w      io.Writer
	level  slog.Level
	prefix string
	attrs  []slog.Attr
	groups []string
}

// NewHandler returns a Handler writing to w. Every line is preceded by prefix,
// which is where the dry-run marker goes.
func NewHandler(w io.Writer, level slog.Level, prefix string) *Handler {
	return &Handler{mu: &sync.Mutex{}, w: w, level: level, prefix: prefix}
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

// WithAttrs qualifies the new attrs with the groups open at the time they are
// added, so that attrs attached before a WithGroup keep their plain keys.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h

	clone.attrs = append([]slog.Attr{}, h.attrs...)
	for _, a := range attrs {
		a.Key = h.qualify(a.Key)
		clone.attrs = append(clone.attrs, a)
	}

	return &clone
}

func (h *Handler) qualify(key string) string {
	if len(h.groups) == 0 {
		return key
	}

	return strings.Join(h.groups, ".") + "." + key
}

func (h *Handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.groups = append(append([]string{}, h.groups...), name)

	return &clone
}

func (h *Handler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(h.prefix)
	sb.WriteString(r.Level.String())
	sb.WriteByte(' ')
	sb.WriteString(r.Message)

	// Stored attrs were qualified when they were added; record attrs take
	// whatever groups are open now.
	for _, a := range h.attrs {
		h.writeAttr(&sb, a, false)
	}

	r.Attrs(func(a slog.Attr) bool {
		h.writeAttr(&sb, a, true)
		return true
	})
	sb.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err := io.WriteString(h.w, sb.String())

	return err
}

func (h *Handler) writeAttr(sb *strings.Builder, a slog.Attr, group bool) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}

	// A group contributes its members under a dotted key, matching how
	// TextHandler renders them.
	if a.Value.Kind() == slog.KindGroup {
		members := a.Value.Group()
		for _, m := range members {
			if a.Key != "" {
				m.Key = a.Key + "." + m.Key
			}

			h.writeAttr(sb, m, group)
		}

		return
	}

	key := a.Key
	if group {
		key = h.qualify(key)
	}

	value := fmt.Sprint(a.Value.Any())
	// Quote only to keep a spaced value readable as one field. The quotes are
	// added raw, so any colour the value carries survives.
	if strings.ContainsAny(value, " \t") {
		value = `"` + value + `"`
	}

	sb.WriteByte(' ')
	sb.WriteString(key)
	sb.WriteByte('=')
	sb.WriteString(value)
}
