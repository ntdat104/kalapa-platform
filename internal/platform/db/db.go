// Package db sở hữu connection pool tới Postgres và bộ chạy migration nhỏ gọn.
//
// Kích thước pool được chọn theo ngân sách bộ nhớ chứ không theo thông lượng:
// với resources.limits.memory là 64Mi và CloudNativePG chạy một instance 256Mi,
// bốn kết nối mỗi pod đã là rộng rãi. Pool phình to là cách phổ biến nhất khiến
// một hệ microservice "nhỏ" làm cạn max_connections.
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
	// Tái tạo kết nối định kỳ ngăn một lần chuyển primary của CloudNativePG để
	// lại pool bị ghim vào instance đã bị giáng cấp.
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open pool: %w", err)
	}
	return &DB{pool}, nil
}

// Ping được nối vào readiness probe, không phải liveness probe.
func (d *DB) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return d.Pool.Ping(ctx)
}

// Migrate chạy lần lượt các câu lệnh trong cùng một transaction.
//
// Cố ý viết vài dòng thay vì dùng một framework migration: trọng tâm của lab là
// cách Kubernetes chạy nó chứ không phải công cụ SQL. Mọi câu lệnh phải
// idempotent — migration sẽ chạy lại ở mỗi lần pod khởi động.
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

// WaitReady chặn cho tới khi Postgres trả lời hoặc context hết hạn. Được gọi
// lúc khởi động để một pod chạy đua với quá trình bootstrap của CloudNativePG
// sẽ thử lại, thay vì crash-loop và đốt sạch ngân sách restart.
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
