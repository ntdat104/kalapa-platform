// Package db owns the Postgres connection pool and the tiny migration runner.
//
// The pool is sized for the memory budget, not for throughput: with
// resources.limits.memory at 64Mi and CloudNativePG running a single 256Mi
// instance, four connections per pod is already generous. Oversized pools are
// the most common way a "small" microservice stack exhausts max_connections.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct{ *pgxpool.Pool }

func Open(ctx context.Context, dsn string, maxConns, minConns int32) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = maxConns
	cfg.MinConns = minConns
	// Recycling connections keeps a rolling CloudNativePG switchover from
	// leaving the pool pinned to a demoted primary.
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	return &DB{pool}, nil
}

// Ping is wired into the readiness probe, not the liveness probe.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return d.Pool.Ping(ctx)
}

// Migrate applies statements in order inside one transaction.
//
// This is intentionally a few lines rather than a migration framework: the
// point of the lab is the Kubernetes Job that runs it (see the `migration`
// hook in the go-service chart), not the SQL tooling. Every statement must be
// idempotent — the Job can and will re-run.
func (d *DB) Migrate(ctx context.Context, statements []string) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for i, stmt := range statements {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("migration statement %d: %w", i, err)
		}
	}
	return tx.Commit(ctx)
}

// WaitReady blocks until Postgres answers or the context expires. Called at
// startup so a pod that races CloudNativePG's bootstrap retries instead of
// crash-looping and burning its restart budget.
func (d *DB) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	backoff := 250 * time.Millisecond
	for {
		if err := d.Ping(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("postgres not ready after %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 4*time.Second {
			backoff *= 2
		}
	}
}
