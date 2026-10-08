// Command kyc runs the KYC intake service.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/kalapa-lab/kalapa-platform/internal/kyc"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/config"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/db"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/events"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/logging"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/obs"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load("kyc")
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
		// Flush the batch span processor before exit, otherwise the spans from
		// the last few seconds of a pod's life — exactly the ones you want
		// when diagnosing a bad rollout — are discarded.
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
	if err := pool.Migrate(ctx, kyc.Schema); err != nil {
		return err
	}
	log.Info("schema applied")

	producer, err := events.NewProducer(cfg.Kafka.Brokers, cfg.Kafka.Topic, tracer, metrics, log)
	if err != nil {
		return err
	}
	defer producer.Close()

	svc := &kyc.Service{DB: pool, Producer: producer, Tracer: tracer, Log: log}

	srv := httpx.New(cfg.Server, log, metrics)
	srv.AddReadinessCheck("postgres", pool.Ping)
	srv.AddReadinessCheck("kafka", producer.Ping)

	return srv.Run(ctx, svc.Routes(), cfg.Service.Name)
}
