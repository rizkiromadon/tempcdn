package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	colorReset  = "\033[0m"
	colorGray   = "\033[90m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
)

// New returns a logger. When stdout is a terminal, it uses a compact,
// colorized, human-readable format. Otherwise (e.g. redirected to a file or
// collected by a log aggregator) it falls back to structured JSON.
func New() *slog.Logger {
	if isTerminal(os.Stdout) {
		return slog.New(newPrettyHandler(os.Stdout, slog.LevelInfo))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

type prettyHandler struct {
	mu     *sync.Mutex
	out    io.Writer
	level  slog.Level
	attrs  []slog.Attr
	groups []string
}

func newPrettyHandler(out io.Writer, level slog.Level) *prettyHandler {
	return &prettyHandler{
		mu:    &sync.Mutex{},
		out:   out,
		level: level,
	}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &prettyHandler{
		mu:     h.mu,
		out:    h.out,
		level:  h.level,
		attrs:  append(append([]slog.Attr{}, h.attrs...), attrs...),
		groups: h.groups,
	}
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	return &prettyHandler{
		mu:     h.mu,
		out:    h.out,
		level:  h.level,
		attrs:  h.attrs,
		groups: append(append([]string{}, h.groups...), name),
	}
}

func (h *prettyHandler) Handle(_ context.Context, record slog.Record) error {
	var b strings.Builder

	b.WriteString(colorGray)
	b.WriteString(record.Time.Format("15:04:05.000"))
	b.WriteString(colorReset)
	b.WriteByte(' ')

	b.WriteString(levelBadge(record.Level))
	b.WriteByte(' ')

	b.WriteString(colorBold)
	b.WriteString(record.Message)
	b.WriteString(colorReset)

	fields := make([]slog.Attr, 0, len(h.attrs)+record.NumAttrs())
	fields = append(fields, h.attrs...)
	record.Attrs(func(a slog.Attr) bool {
		fields = append(fields, a)
		return true
	})

	for _, a := range fields {
		b.WriteByte(' ')
		b.WriteString(colorCyan)
		key := a.Key
		if len(h.groups) > 0 {
			key = strings.Join(h.groups, ".") + "." + key
		}
		b.WriteString(key)
		b.WriteString(colorReset)
		b.WriteByte('=')
		b.WriteString(formatValue(a.Value))
	}

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

func levelBadge(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return colorRed + colorBold + "ERROR" + colorReset
	case level >= slog.LevelWarn:
		return colorYellow + colorBold + "WARN " + colorReset
	case level >= slog.LevelInfo:
		return colorGreen + colorBold + "INFO " + colorReset
	default:
		return colorBlue + colorBold + "DEBUG" + colorReset
	}
}

func formatValue(v slog.Value) string {
	v = v.Resolve()
	switch v.Kind() {
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindDuration:
		return v.Duration().String()
	default:
		s := fmt.Sprintf("%v", v.Any())
		if strings.ContainsAny(s, " \t\"") {
			return fmt.Sprintf("%q", s)
		}
		return s
	}
}
