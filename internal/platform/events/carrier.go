package events

import "github.com/twmb/franz-go/pkg/kgo"

// RecordCarrier biến header của một bản ghi Kafka thành giao diện
// TextMapCarrier của OTel — thứ mà `otel.GetTextMapPropagator().Inject/Extract`
// cần. Nhờ nó, một bản ghi mang theo `traceparent` y như cách một request HTTP
// mang nó trong header.
type RecordCarrier struct{ Record *kgo.Record }

func (c *RecordCarrier) Get(key string) string {
	for _, h := range c.Record.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

// Set GHI ĐÈ header đã có thay vì nối thêm, để một bản ghi được phát lại (do
// retry hoặc replay) không tích luỹ nhiều traceparent trùng nhau.
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
