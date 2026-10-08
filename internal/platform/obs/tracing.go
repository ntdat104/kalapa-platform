// Package obs nối hai tín hiệu telemetry mà bản thân service Go tự phát ra:
// trace OTLP đẩy sang collector, và metric Prometheus được kéo từ port quản
// trị. Log là tín hiệu thứ ba và đi ra qua stdout (xem package logging), vì đó
// là đường duy nhất còn sống sót khi process đang chết.
package obs

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TracingOptions tương ứng khối `tracing` trong file cấu hình được mount.
type TracingOptions struct {
	Enabled     bool
	Endpoint    string // host:port of the collector's OTLP/gRPC receiver
	ServiceName string
	Version     string
	Environment string
	SampleRatio float64
}

// InitTracing trả về một tracer và một hàm tắt. Khi tracing bị tắt (hoặc chưa
// cấu hình endpoint), nó trả về tracer no-op để không chỗ gọi nào phải kiểm tra
// nil — service vẫn chạy được ngay cả khi xoá sạch namespace observability, mà
// đó đúng là tình huống hỏng bạn muốn mô phỏng được trong lab.
func InitTracing(ctx context.Context, o TracingOptions) (trace.Tracer, func(context.Context) error, error) {
	// Propagator W3C phải được cài kể cả khi tracing tắt: gateway vẫn chuyển
	// tiếp traceparent mà nó nhận được, nên một chặng bị tắt trace sẽ thành một
	// khoảng trống trong trace chứ không thành hai trace rời rạc.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	if !o.Enabled || o.Endpoint == "" {
		return noop.NewTracerProvider().Tracer(o.ServiceName), func(context.Context) error { return nil }, nil
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	exporter, err := otlptracegrpc.New(dialCtx,
		otlptracegrpc.WithEndpoint(o.Endpoint),
		// Dùng plaintext ở đây là đúng: chặng này là pod -> collector bên trong
		// cluster. Việc kết thúc TLS là nhiệm vụ của collector, không phải của ta.
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp exporter: %w", err)
	}

	// sdkresource.Merge từ chối gộp hai resource có schema URL khác nhau, và
	// sdkresource.Default() mang theo đúng phiên bản semconv mà SDK được build
	// cùng. Nên import semconv bên dưới phải bám theo SDK chứ không được ghim
	// độc lập — nếu không, process chết ngay lúc khởi động với lỗi
	// "conflicting Schema URL", mà không test nào bỏ qua tracing bắt được.
	res, err := sdkresource.Merge(sdkresource.Default(), sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(o.ServiceName),
		semconv.ServiceVersion(o.Version),
		semconv.DeploymentEnvironmentNameKey.String(o.Environment),
		// Các thuộc tính k8s.* đến từ biến môi trường Downward API do chart đặt;
		// chính chúng làm service graph của Tempo khớp được với Prometheus.
		attribute.String("k8s.namespace.name", envOr("K8S_NAMESPACE", "")),
		attribute.String("k8s.pod.name", envOr("K8S_POD_NAME", "")),
		attribute.String("k8s.node.name", envOr("K8S_NODE_NAME", "")),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("otel resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithMaxQueueSize(2048),
			sdktrace.WithBatchTimeout(2*time.Second),
		),
		sdktrace.WithResource(res),
		// ParentBased giữ quyết định lấy mẫu nhất quán cho cả request; lấy mẫu
		// riêng ở từng chặng sẽ sinh ra nửa trace.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
	)
	otel.SetTracerProvider(tp)

	return tp.Tracer(o.ServiceName), tp.Shutdown, nil
}
