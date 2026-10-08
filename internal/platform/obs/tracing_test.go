package obs

import (
	"context"
	"testing"
	"time"
)

// InitTracing khi có endpoint sẽ dựng resource của OTel, mà resource.Merge thì
// từ chối gộp hai resource có schema URL khác nhau. Vì vậy ghim import semconv
// độc lập với phiên bản SDK sẽ giết process ngay lúc khởi động với lỗi
// "conflicting Schema URL" — một kiểu hỏng mà không test nào để tracing tắt có
// thể thấy được.
//
// Test này chạy riêng đường dựng resource. Không có collector nào lắng nghe,
// nhờ đó nó cũng ghim lại một hành vi thứ hai đáng biết: việc tạo exporter sẽ
// chờ hết ngân sách kết nối chứ không thất bại ngay, nên một pod có collector
// chết sẽ khởi động chậm hơn khoảng 10 giây. Ngân sách của startupProbe che
// được khoảng đó; còn context cha ngắn ở đây giữ cho test chạy nhanh.
func TestInitTracingBuildsAValidResource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	tracer, shutdown, err := InitTracing(ctx, TracingOptions{
		Enabled:     true,
		Endpoint:    "127.0.0.1:4317",
		ServiceName: "test-service",
		Version:     "v0.0.1",
		Environment: "test",
		SampleRatio: 1.0,
	})
	if err != nil {
		t.Fatalf("InitTracing: %v", err)
	}
	// Có giới hạn thời gian, giống cmd/*/main.go: shutdown sẽ flush bộ xử lý
	// theo lô, và khi không có ai lắng nghe thì lần flush đó sẽ đốt hết ngân
	// sách thử lại của exporter.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = shutdown(stopCtx)
	})

	if tracer == nil {
		t.Fatal("tracer is nil")
	}
	// Mở một span là cách chạy thử provider từ đầu tới cuối.
	_, span := tracer.Start(context.Background(), "probe")
	span.End()
}

// Khi tracing tắt, bên gọi vẫn phải nhận được một tracer dùng được, để không
// chỗ gọi nào phải kiểm tra nil và các service vẫn chạy được ngay cả khi xoá
// sạch namespace observability.
func TestInitTracingDisabledReturnsNoopTracer(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts TracingOptions
	}{
		{"explicitly disabled", TracingOptions{Enabled: false, Endpoint: "127.0.0.1:4317"}},
		{"enabled but no endpoint", TracingOptions{Enabled: true, Endpoint: ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracer, shutdown, err := InitTracing(context.Background(), tc.opts)
			if err != nil {
				t.Fatalf("InitTracing: %v", err)
			}
			if tracer == nil {
				t.Fatal("tracer is nil — call sites would panic")
			}
			_, span := tracer.Start(context.Background(), "probe")
			span.End()
			if err := shutdown(context.Background()); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
		})
	}
}
