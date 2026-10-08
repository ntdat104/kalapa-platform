// Package events là phần tiếp giáp với Kafka: một producer, một vòng lặp
// consumer, và phần truyền trace context giữ cho trace của một request không
// bị đứt khi đi qua chặng bất đồng bộ.
//
// Phần đáng chú ý nằm ở carrier.go. Truyền context qua HTTP là miễn phí
// (otelhttp lo), nhưng không có gì tự instrument Kafka cho bạn: nếu producer
// không ghi `traceparent` vào header của bản ghi và consumer không đọc nó ra,
// Tempo sẽ hiện hai trace không liên quan và service graph mất cạnh
// kyc -> scoring. Đó là mảnh hay bị bỏ sót nhất trong một hệ tracing
// microservice, nên nó được nối tường minh ở đây thay vì giấu trong một lớp bọc.
package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/obs"
)

// Producer phát các sự kiện nghiệp vụ.
type Producer struct {
	client  *kgo.Client
	topic   string
	tracer  trace.Tracer
	metrics *obs.Metrics
	log     *slog.Logger
}

func NewProducer(brokers []string, topic string, tracer trace.Tracer, metrics *obs.Metrics, log *slog.Logger) (*Producer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.DefaultProduceTopic(topic),
		// franz-go bật sẵn chế độ phát idempotent; acks=all làm điều đó có ý
		// nghĩa trên cluster nhiều broker. Trên cluster lab một broker thì nó
		// chẳng tốn gì và giữ cho cấu hình trung thực.
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchMaxBytes(1<<20),
		kgo.RecordRetries(5),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{client: client, topic: topic, tracer: tracer, metrics: metrics, log: log}, nil
}

// Publish mã hoá payload thành JSON và gửi ĐỒNG BỘ, để HTTP handler gọi nó
// biết được thất bại thật, thay vì nói dối người gọi về một lần ghi chưa hề
// xảy ra.
func (p *Producer) Publish(ctx context.Context, key string, payload any) error {
	ctx, span := p.tracer.Start(ctx, "produce "+p.topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			semconv.MessagingSystemKafka,
			semconv.MessagingDestinationName(p.topic),
			attribute.String("messaging.kafka.message.key", key),
		),
	)
	defer span.End()

	body, err := json.Marshal(payload)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "marshal")
		return fmt.Errorf("marshal event: %w", err)
	}

	rec := &kgo.Record{Topic: p.topic, Key: []byte(key), Value: body}
	// Inject SAU khi span producer đã bắt đầu: consumer phải nối vào span này,
	// không phải vào span HTTP đứng trước nó.
	otel.GetTextMapPropagator().Inject(ctx, &RecordCarrier{Record: rec})

	if err := p.client.ProduceSync(ctx, rec).FirstErr(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "produce")
		p.metrics.RecordEvent(p.topic, "produce", "error")
		return fmt.Errorf("produce: %w", err)
	}

	p.metrics.RecordEvent(p.topic, "produce", "success")
	p.log.InfoContext(ctx, "event published",
		slog.String("topic", p.topic), slog.String("key", key))
	return nil
}

// Ping là phép kiểm tra readiness: nó hỏi cluster lấy metadata chứ không chỉ
// kiểm tra socket TCP — nhờ vậy bắt được tình huống "broker đã lên nhưng
// partition của topic chưa có leader".
func (p *Producer) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return p.client.Ping(ctx)
}

func (p *Producer) Close() { p.client.Close() }

// Handler xử lý một sự kiện đã giải mã. Trả về lỗi nghĩa là bản ghi thất bại;
// consumer ghi log rồi vẫn tiến offset, vì một bản ghi độc không được phép làm
// kẹt partition trên cluster lab vốn không có dead-letter queue.
type Handler func(ctx context.Context, key string, value []byte) error

// Consumer là vòng lặp consumer-group chạy trên một goroutine duy nhất.
type Consumer struct {
	client  *kgo.Client
	topic   string
	tracer  trace.Tracer
	metrics *obs.Metrics
	log     *slog.Logger
}

func NewConsumer(brokers []string, topic, group string, tracer trace.Tracer, metrics *obs.Metrics, log *slog.Logger) (*Consumer, error) {
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(group),
		kgo.ConsumeTopics(topic),
		// Chỉ commit SAU khi handler thành công (at-least-once). Autocommit
		// thực chất là at-most-once đội lốt: một pod bị evict giữa chừng sẽ làm
		// mất sự kiện mà không ai hay.
		kgo.DisableAutoCommit(),
		kgo.FetchMaxBytes(1<<20),
		// Timeout rebalance ngắn giúp một lần rolling restart không treo cả
		// group 45 giây trong lúc pod mới chờ được giao partition.
		kgo.SessionTimeout(20*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer: %w", err)
	}
	return &Consumer{client: client, topic: topic, tracer: tracer, metrics: metrics, log: log}, nil
}

func (c *Consumer) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.client.Ping(ctx)
}

// Run chặn cho tới khi ctx bị huỷ. Hãy truyền nó vào httpx.Server.Go để vòng
// đời của nó khớp với các listener HTTP, và SIGTERM xả cả hai cùng lúc.
func (c *Consumer) Run(ctx context.Context, handle Handler) error {
	defer c.client.Close()
	c.log.Info("kafka consumer started", slog.String("topic", c.topic))

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			// Commit nốt phần đã xử lý xong trước khi bắt đầu xả, để pod thay
			// thế không xử lý lại phần đó.
			commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := c.client.CommitUncommittedOffsets(commitCtx)
			cancel()
			if err != nil {
				c.log.Warn("final offset commit failed", slog.Any("error", err))
			}
			return ctx.Err()
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				c.log.Error("fetch error",
					slog.String("topic", e.Topic), slog.Any("error", e.Err))
			}
			continue
		}

		fetches.EachRecord(func(rec *kgo.Record) {
			c.process(ctx, rec, handle)
		})

		if err := c.client.CommitUncommittedOffsets(ctx); err != nil {
			c.log.Error("offset commit failed", slog.Any("error", err))
		}
	}
}

func (c *Consumer) process(ctx context.Context, rec *kgo.Record, handle Handler) {
	// Extract trước tiên: việc này khôi phục trace của producer, nhờ đó span
	// consumer trở thành con của nó và trace trải qua cả hai service.
	ctx = otel.GetTextMapPropagator().Extract(ctx, &RecordCarrier{Record: rec})

	ctx, span := c.tracer.Start(ctx, "consume "+rec.Topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			semconv.MessagingSystemKafka,
			semconv.MessagingDestinationName(rec.Topic),
			attribute.Int64("messaging.kafka.message.offset", rec.Offset),
			attribute.Int("messaging.kafka.destination.partition", int(rec.Partition)),
		),
	)
	defer span.End()

	if err := handle(ctx, string(rec.Key), rec.Value); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		c.metrics.RecordEvent(rec.Topic, "consume", "error")
		c.log.ErrorContext(ctx, "event handling failed",
			slog.String("topic", rec.Topic),
			slog.Int64("offset", rec.Offset),
			slog.Any("error", err))
		return
	}
	c.metrics.RecordEvent(rec.Topic, "consume", "success")
}
