package events

import "github.com/twmb/franz-go/pkg/kgo"

// RecordCarrier adapts a Kafka record's headers to the OTel TextMapCarrier
// interface, which is what `otel.GetTextMapPropagator().Inject/Extract`
// expects. With it, a record carries `traceparent` the same way an HTTP
// request carries it in a header.
type RecordCarrier struct{ Record *kgo.Record }

func (c *RecordCarrier) Get(key string) string {
	for _, h := range c.Record.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Set replaces an existing header rather than appending, so a record that is
// re-produced (a retry, a replay) does not accumulate duplicate traceparents.
func (c *RecordCarrier) Set(key, value string) {
	for i, h := range c.Record.Headers {
		if h.Key == key {
			c.Record.Headers[i].Value = []byte(value)
			return
		}
	}
	c.Record.Headers = append(c.Record.Headers, kgo.RecordHeader{Key: key, Value: []byte(value)})
}

func (c *RecordCarrier) Keys() []string {
	keys := make([]string, 0, len(c.Record.Headers))
	for _, h := range c.Record.Headers {
		keys = append(keys, h.Key)
	}
	return keys
}
