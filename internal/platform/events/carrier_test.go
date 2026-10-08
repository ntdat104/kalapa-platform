package events

import (
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Carrier là bản lề mà cả trace xuyên service treo lên, nên giao kèo của nó
// với propagator của OTel rất đáng được ghim lại bằng test.
func TestRecordCarrierRoundTrip(t *testing.T) {
	c := &RecordCarrier{Record: &kgo.Record{}}

	c.Set("traceparent", "00-aaaa-bbbb-01")
	if got := c.Get("traceparent"); got != "00-aaaa-bbbb-01" {
		t.Fatalf("Get = %q", got)
	}

	// Phát lại một bản ghi phải GHI ĐÈ chứ không nối thêm: hai header
	// traceparent sẽ khiến Extract nhặt đại một cái.
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
