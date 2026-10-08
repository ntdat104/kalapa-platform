// Package obs wires the two telemetry signals a Go service emits itself:
// OTLP traces pushed to the collector, and Prometheus metrics pulled from the
// admin port. Logs are the third signal and leave via stdout (see package
// logging) because that is the only path that survives a crashing process.
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

// TracingOptions mirrors the `tracing` block of the mounted config.
type TracingOptions struct {
	Enabled     bool
	Endpoint    string // host:port of the collector's OTLP/gRPC receiver
	ServiceName string
	Version     string
	Environment string
	SampleRatio float64
}

// InitTracing returns a tracer and a shutdown func. When tracing is disabled
// (or no endpoint is configured) it hands back a no-op tracer so call sites
// never need a nil check — the service stays runnable with the whole
// observability namespace deleted, which is exactly the failure you want to be
// able to simulate in a lab.
func InitTracing(ctx context.Context, o TracingOptions) (trace.Tracer, func(context.Context) error, error) {
	// The W3C propagator must be installed even when tracing is off: the
	// gateway still forwards whatever traceparent it received, so a disabled
	// hop becomes a gap in the trace rather than two disconnected traces.
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
		// Plaintext is correct here: the hop is pod -> collector inside the
		// cluster. Terminating TLS would be the collector's job, not ours.
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp exporter: %w", err)
	}

	// sdkresource.Merge refuses to combine resources whose schema URLs differ,
	// and sdkresource.Default() carries whichever semconv version the SDK was
	// built against. So the semconv import below must track the SDK, not be
	// pinned independently — otherwise the process dies at startup with
	// "conflicting Schema URL", which no test that skips tracing will catch.
	res, err := sdkresource.Merge(sdkresource.Default(), sdkresource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(o.ServiceName),
		semconv.ServiceVersion(o.Version),
		semconv.DeploymentEnvironmentNameKey.String(o.Environment),
		// k8s.* attributes come from the Downward API env vars the chart sets,
		// which is what lets Tempo's service graph line up with Prometheus.
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
		// ParentBased keeps a sampling decision consistent across the whole
		// request; sampling per hop would produce half-traces.
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(o.SampleRatio))),
	)
	otel.SetTracerProvider(tp)

	return tp.Tracer(o.ServiceName), tp.Shutdown, nil
}
