// Command scoring runs the credit-scoring consumer and its query API.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/config"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/db"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/events"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/logging"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/obs"
	"github.com/kalapa-lab/kalapa-platform/internal/scoring"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load("scoring")
	if err != nil {
		return err
	}
	log := logging.New(cfg.Service.Name, cfg.Service.Version, cfg.Service.Environment, cfg.Service.LogLevel)
	metrics := obs.NewMetrics(cfg.Service.Name, cfg.Service.Version)

	tracer, shutdownTracing, err := obs.InitTracing(ctx, obs.TracingOptions{
		Enabled:     cfg.Tracing.Enabled,
		Endpoint:    cfg.Tracing.OTLPEndpoint,
		ServiceName: cfg.Service.Name,
		Version:     cfg.Service.Version,
		Environment: cfg.Service.Environment,
		SampleRatio: cfg.Tracing.SampleRatio,
	})
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(flushCtx)
	}()

	pool, err := db.Open(ctx, cfg.Database.DSN(), cfg.Database.MaxConns, cfg.Database.MinConns)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.WaitReady(ctx, 90*time.Second); err != nil {
		return err
	}
	if err := pool.Migrate(ctx, scoring.Schema); err != nil {
		return err
	}
	log.Info("schema applied")

	consumer, err := events.NewConsumer(cfg.Kafka.Brokers, cfg.Kafka.Topic, cfg.Kafka.ConsumerGroup, tracer, metrics, log)
	if err != nil {
		return err
	}

	svc := &scoring.Service{DB: pool, Tracer: tracer, Log: log}

	srv := httpx.New(cfg.Server, log, metrics)
	srv.AddReadinessCheck("postgres", pool.Ping)
	srv.AddReadinessCheck("kafka", consumer.Ping)
	// The consumer loop lives and dies with the HTTP listeners, so SIGTERM
	// drains both and the final offset commit happens inside the grace period.
	srv.Go(func(ctx context.Context) error { return consumer.Run(ctx, svc.Handle) })

	return srv.Run(ctx, svc.Routes(), cfg.Service.Name)
}
