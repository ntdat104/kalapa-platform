// Package events is the Kafka edge: a producer, a consumer loop, and the
// trace-context plumbing that keeps a request's trace intact across the async
// hop.
//
// The interesting part is carrier.go. HTTP propagation is free (otelhttp does
// it), but nothing instruments Kafka for you: if the producer does not write
// `traceparent` into the record headers and the consumer does not read it back
// out, Tempo shows two unrelated traces and the service graph loses the edge
// kyc -> scoring. That is the single most common gap in a microservice tracing
// setup, so it is wired explicitly here rather than hidden in a wrapper.
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

// Producer publishes domain events.
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
		// Idempotent production is on by default in franz-go; acks=all makes
		// it meaningful on a multi-broker cluster. On the single-broker lab
		// cluster it costs nothing and keeps the config honest.
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchMaxBytes(1<<20),
		kgo.RecordRetries(5),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{client: client, topic: topic, tracer: tracer, metrics: metrics, log: log}, nil
}

// Publish serialises payload as JSON and sends it synchronously, so the HTTP
// handler that called it can report a real failure instead of lying to the
// caller about a write that never landed.
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
	// Inject AFTER the producer span starts: the consumer must link to this
	// span, not to the HTTP span that preceded it.
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

// Ping is the readiness check: it asks the cluster for metadata rather than
// just checking the TCP socket, which is what catches "broker up, topic's
// partitions have no leader yet".
func (p *Producer) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return p.client.Ping(ctx)
}

func (p *Producer) Close() { p.client.Close() }

// Handler processes one decoded event. Returning an error marks the record as
// failed; the consumer logs it and still advances the offset, because a
// poison message must not wedge the partition on a lab cluster with no DLQ.
type Handler func(ctx context.Context, key string, value []byte) error

// Consumer is a single-goroutine consumer-group loop.
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
		// Commit only after the handler succeeds (at-least-once). Autocommit
		// would be at-most-once in disguise: a pod evicted mid-handler would
		// drop the event silently.
		kgo.DisableAutoCommit(),
		kgo.FetchMaxBytes(1<<20),
		// A short rebalance timeout keeps a rolling restart from parking the
		// group for 45s while the new pod waits to be assigned partitions.
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

// Run blocks until ctx is cancelled. Pass it to httpx.Server.Go so its
// lifetime matches the HTTP listeners' and SIGTERM drains both together.
func (c *Consumer) Run(ctx context.Context, handle Handler) error {
	defer c.client.Close()
	c.log.Info("kafka consumer started", slog.String("topic", c.topic))

	for {
		fetches := c.client.PollFetches(ctx)
		if ctx.Err() != nil {
			// Commit whatever completed before the drain started so the
			// replacement pod does not reprocess it.
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
	// Extract first: this restores the producer's trace so the consumer span
	// becomes a child of it and the trace spans both services.
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
