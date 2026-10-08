// Package logging sinh ra log JSON có cấu trúc để Grafana Alloy đẩy thẳng vào
// Loki mà không cần bước phân tích nào, và để Grafana nhảy từ một dòng log
// sang đúng trace tương ứng.
//
// Giao kèo với hệ observability gồm ba trường:
//
//	level     -> được Alloy nâng thành label của Loki
//	trace_id  -> khớp bởi regex derivedField của Loki datasource -> Tempo
//	span_id   -> thu hẹp cú nhảy xuống đúng span
//
// YAS làm điều tương tự bằng pattern `traceId=%X{traceId:-}` của logback cộng
// thêm MDC appender của OTel; trong Go ta đọc thẳng các ID từ context, nên bỏ
// hẳn được vấn đề MDC/thread-local.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

type traceHandler struct{ slog.Handler }

// Handle gắn thêm trace id và span id đang hoạt động vào mọi bản ghi. Vì
// handler này nằm trên cùng chuỗi xử lý, nó phủ cả log do thư viện bên ngoài
// sinh ra khi chúng chỉ có context.
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

// New dựng logger cho cả process và đặt nó làm mặc định của slog, để mọi
// package gọi slog.InfoContext đều ra cùng một định dạng.
func New(service, version, env, level string) *slog.Logger {
	base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: parseLevel(level),
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// "msg" và "time" là hai khoá duy nhất được bộ phân tích JSON của
			// Loki xử lý đặc biệt trong các dashboard kèm theo repo này; đổi
			// tên chúng ở đây sẽ âm thầm làm hỏng các panel đó.
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
