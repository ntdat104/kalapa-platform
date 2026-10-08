package events

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// The carrier is the hinge the whole cross-service trace hangs on, so its
// contract with the OTel propagator is worth pinning down.
func TestRecordCarrierRoundTrip(t *testing.T) {
	c := &RecordCarrier{Record: &kgo.Record{}}

	c.Set("traceparent", "00-aaaa-bbbb-01")
	if got := c.Get("traceparent"); got != "00-aaaa-bbbb-01" {
		t.Fatalf("Get = %q", got)
	}

	// Re-producing a record must overwrite, not append: two traceparent
	// headers would make Extract pick an arbitrary one.
	c.Set("traceparent", "00-cccc-dddd-01")
	if n := len(c.Record.Headers); n != 1 {
		t.Fatalf("expected 1 header after overwrite, got %d", n)
	}
	if got := c.Get("traceparent"); got != "00-cccc-dddd-01" {
		t.Fatalf("overwrite failed, Get = %q", got)
	}

	c.Set("baggage", "tenant=kalapa")
	keys := c.Keys()
	if len(keys) != 2 {
		t.Fatalf("Keys() = %v", keys)
	}
	if c.Get("missing") != "" {
		t.Fatal("Get on absent key must return empty string")
	}
}
