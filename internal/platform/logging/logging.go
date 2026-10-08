// Package logging produces structured JSON logs that Grafana Alloy can ship to
// Loki without any parsing stage, and that Grafana can pivot from a log line
// straight into the matching trace.
//
// The contract with the observability stack is three fields:
//
//	level     -> promoted to a Loki label by the Alloy pipeline
//	trace_id  -> matched by the Loki datasource's derivedField regex -> Tempo
//	span_id   -> narrows the jump to the exact span
//
// YAS does the same thing through logback's `traceId=%X{traceId:-}` pattern
// plus an OTel MDC appender; in Go we read the IDs off the context directly,
// which removes the MDC/thread-local problem entirely.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

type traceHandler struct{ slog.Handler }

// Handle enriches every record with the active trace and span id. Because the
// handler sits at the top of the chain it also covers logs emitted by library
// code that only has a context.
func (h traceHandler) Handle(ctx context.Context, rec slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		rec.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, rec)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

// New builds the process logger and installs it as the slog default so any
// package that calls slog.InfoContext gets the same format.
func New(service, version, env, level string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// "msg" and "time" are the only keys Loki's JSON parser treats
			// specially in the dashboards shipped with this repo; renaming
			// them here would silently break those panels.
			if a.Key == slog.SourceKey {
				return slog.Attr{}
			}
			return a
		},
	})
	logger := slog.New(traceHandler{base}).With(
		slog.String("service", service),
		slog.String("version", version),
		slog.String("env", env),
	)
	slog.SetDefault(logger)
	return logger
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
