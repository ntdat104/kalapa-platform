package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLayersFileThenEnv(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "config.yaml")
	os.WriteFile(base, []byte(`
service:
  environment: staging
  logLevel: warn
server:
  httpPort: 7000
database:
  url: postgres://db:5432/kyc
kafka:
  topic: from-file
`), 0o600)

	overlay := filepath.Join(dir, "overlay.yaml")
	os.WriteFile(overlay, []byte("kafka:\n  topic: from-overlay\n"), 0o600)

	t.Setenv("CONFIG_FILE", base)
	// The overlay must beat the base file, and a missing third file must not
	// be fatal — that is what makes `go run` work with no ConfigMap mounted.
	t.Setenv("CONFIG_EXTRA_FILES", overlay+",/nonexistent/also-fine.yaml")
	t.Setenv("POSTGRES_PASSWORD", "s3cret")
	t.Setenv("HTTP_PORT", "8081")

	cfg, err := Load("kyc")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Service.Name != "kyc" {
		t.Errorf("name = %q", cfg.Service.Name)
	}
	if cfg.Service.Environment != "staging" {
		t.Errorf("environment = %q, file layer lost", cfg.Service.Environment)
	}
	if cfg.Kafka.Topic != "from-overlay" {
		t.Errorf("topic = %q, overlay did not win", cfg.Kafka.Topic)
	}
	if cfg.Server.HTTPPort != 8081 {
		t.Errorf("httpPort = %d, env did not win over file", cfg.Server.HTTPPort)
	}
	if cfg.Server.AdminPort != 9090 {
		t.Errorf("adminPort = %d, default lost", cfg.Server.AdminPort)
	}
}

func TestDSNAppendsCredentials(t *testing.T) {
	d := Database{URL: "postgres://pg:5432/kyc", Username: "app", Password: "pw"}
	if got, want := d.DSN(), "postgres://pg:5432/kyc?user=app&password=pw"; got != want {
		t.Errorf("DSN() = %q, want %q", got, want)
	}

	d2 := Database{URL: "postgres://pg:5432/kyc?sslmode=disable", Username: "app"}
	if got, want := d2.DSN(), "postgres://pg:5432/kyc?sslmode=disable&user=app"; got != want {
		t.Errorf("DSN() with existing query = %q, want %q", got, want)
	}

	if (Database{}).DSN() != "" {
		t.Error("empty URL must yield empty DSN")
	}
}
