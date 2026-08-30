package observability

import (
	"context"
	"io"
	"log/slog"
	"runtime/debug"
)

// NewJSONLogger keeps logs machine-readable and attaches a stack to every
// error-level record. Provider errors and identifiers remain structured fields;
// callers must never attach secrets or raw customer payloads.
func NewJSONLogger(output io.Writer, level slog.Level) *slog.Logger {
	base := slog.NewJSONHandler(output, &slog.HandlerOptions{Level: level})
	return slog.New(stackHandler{next: base})
}

type stackHandler struct{ next slog.Handler }

func (h stackHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h stackHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Level >= slog.LevelError {
		record.AddAttrs(slog.String("stack", string(debug.Stack())))
	}
	return h.next.Handle(ctx, record)
}

func (h stackHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return stackHandler{next: h.next.WithAttrs(attrs)}
}

func (h stackHandler) WithGroup(name string) slog.Handler {
	return stackHandler{next: h.next.WithGroup(name)}
}
