// Command gateway runs the public edge service.
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/kalapa-lab/kalapa-platform/internal/gateway"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/config"
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

	cfg, err := config.Load("gateway")
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

	svc := &gateway.Service{
		KYCURL:     cfg.Services.KYC,
		ScoringURL: cfg.Services.Scoring,
		Tracer:     tracer,
		Log:        log,
		Auth:       gateway.NewAuthenticator(cfg.Auth.Enabled, cfg.Auth.IssuerURL, cfg.Auth.Audience, log),
	}

	srv := httpx.New(cfg.Server, log, metrics)
	// The gateway is stateless: it has no readiness dependency on the
	// upstreams on purpose. Making it unready when kyc is down would take the
	// whole edge offline, including the endpoints that do not need kyc.
	log.Info("upstreams configured",
		slog.String("kyc", cfg.Services.KYC),
		slog.String("scoring", cfg.Services.Scoring),
		slog.Bool("auth_enabled", cfg.Auth.Enabled))
	return srv.Run(ctx, svc.Routes(), cfg.Service.Name)
}
