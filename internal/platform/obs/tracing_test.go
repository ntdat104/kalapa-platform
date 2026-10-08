package obs

import (
	"context"
	"testing"
	"time"
)

// InitTracing with an endpoint builds the OTel resource, and resource.Merge
// refuses to combine resources whose schema URLs differ. Pinning the semconv
// import independently of the SDK version therefore kills the process at
// startup with "conflicting Schema URL" — a failure no test that leaves
// tracing disabled will ever see.
//
// This test exercises the resource-building path specifically. No collector is
// listening, which also pins down a second behaviour worth knowing: exporter
// construction waits out its dial budget rather than failing, so a pod whose
// collector is down starts ~10s slower. The startupProbe's budget covers that;
// the short parent context here keeps the test fast.
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
	// Bounded, like cmd/*/main.go: shutdown flushes the batch processor, and
	// with nothing listening that flush would otherwise burn the exporter's
	// full retry budget.
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = shutdown(stopCtx)
	})

	if tracer == nil {
		t.Fatal("tracer is nil")
	}
	// Starting a span exercises the provider end to end.
	_, span := tracer.Start(context.Background(), "probe")
	span.End()
}

// With tracing off the caller must still get a usable tracer, so no call site
// needs a nil check and the services stay runnable with the whole
// observability namespace deleted.
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
