// Package config loads service configuration using the same layering strategy
// the YAS reference stack uses for Spring Boot:
//
//  1. built-in defaults (compiled in)
//  2. a YAML file mounted from a ConfigMap   (CONFIG_FILE, default /etc/kalapa/config.yaml)
//  3. an optional per-service YAML overlay   (CONFIG_EXTRA_FILES, comma separated)
//  4. environment variables / Secret refs    (highest precedence)
//
// Layers 2 and 3 mirror SPRING_CONFIG_ADDITIONAL_LOCATION; layer 4 mirrors
// envFrom: secretRef. Keeping credentials in layer 4 only is deliberate — the
// ConfigMap is world-readable inside the namespace, the Secret is not.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the union of every knob any Kalapa service understands. Services
// ignore the sections they do not use; one struct keeps the mounted
// kalapa-configuration ConfigMap identical across all of them.
type Config struct {
	Service  Service  `yaml:"service"`
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Kafka    Kafka    `yaml:"kafka"`
	Tracing  Tracing  `yaml:"tracing"`
	Auth     Auth     `yaml:"auth"`
	Services Upstream `yaml:"services"`
}

type Service struct {
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Environment string `yaml:"environment"`
	LogLevel    string `yaml:"logLevel"`
}

type Server struct {
	// HTTPPort serves business traffic and is the only port behind the Service's
	// `http` port. AdminPort serves /healthz/* and /metrics and is never exposed
	// through the Ingress — same split as YAS's server.port vs management.server.port.
	HTTPPort        int           `yaml:"httpPort"`
	AdminPort       int           `yaml:"adminPort"`
	ReadTimeout     time.Duration `yaml:"readTimeout"`
	WriteTimeout    time.Duration `yaml:"writeTimeout"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
}

type Database struct {
	// URL is a libpq/pgx DSN. Username and Password are injected separately so
	// the non-secret half can live in the ConfigMap and only the credentials
	// come from the Secret.
	URL      string `yaml:"url"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	MaxConns int32  `yaml:"maxConns"`
	MinConns int32  `yaml:"minConns"`
}

type Kafka struct {
	Brokers       []string `yaml:"brokers"`
	ConsumerGroup string   `yaml:"consumerGroup"`
	Topic         string   `yaml:"topic"`
}

type Tracing struct {
	Enabled      bool    `yaml:"enabled"`
	OTLPEndpoint string  `yaml:"otlpEndpoint"`
	SampleRatio  float64 `yaml:"sampleRatio"`
}

type Auth struct {
	// Enabled toggles JWT verification at the gateway. Off by default so the
	// stack is usable before Keycloak finishes its first start.
	Enabled   bool   `yaml:"enabled"`
	IssuerURL string `yaml:"issuerUrl"`
	Audience  string `yaml:"audience"`
}

type Upstream struct {
	KYC     string `yaml:"kyc"`
	Scoring string `yaml:"scoring"`
}

func defaults() Config {
	return Config{
		Service: Service{Version: "dev", Environment: "local", LogLevel: "info"},
		Server: Server{
			HTTPPort:        8080,
			AdminPort:       9090,
			ReadTimeout:     10 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 20 * time.Second,
		},
		Database: Database{MaxConns: 4, MinConns: 0},
		Kafka:    Kafka{Brokers: []string{"kalapa-kafka-bootstrap.kafka:9092"}},
		Tracing:  Tracing{Enabled: true, SampleRatio: 1.0},
	}
}

// Load resolves all four layers. serviceName seeds Service.Name when neither
// the YAML nor SERVICE_NAME provides one.
func Load(serviceName string) (Config, error) {
	cfg := defaults()
	cfg.Service.Name = serviceName

	files := []string{envOr("CONFIG_FILE", "/etc/kalapa/config.yaml")}
	if extra := os.Getenv("CONFIG_EXTRA_FILES"); extra != "" {
		files = append(files, strings.Split(extra, ",")...)
	}
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		raw, err := os.ReadFile(f)
		if os.IsNotExist(err) {
			// Absent files are not fatal: `go run ./cmd/kyc` on a laptop has no
			// ConfigMap, and an optional overlay is optional by definition.
			continue
		}
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", f, err)
		}
		if err := yaml.Unmarshal(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", f, err)
		}
	}

	applyEnv(&cfg)
	if cfg.Service.Name == "" {
		return cfg, fmt.Errorf("service name is empty")
	}
	return cfg, nil
}

func applyEnv(c *Config) {
	strEnv("SERVICE_NAME", &c.Service.Name)
	strEnv("SERVICE_VERSION", &c.Service.Version)
	strEnv("ENVIRONMENT", &c.Service.Environment)
	strEnv("LOG_LEVEL", &c.Service.LogLevel)

	intEnv("HTTP_PORT", &c.Server.HTTPPort)
	intEnv("ADMIN_PORT", &c.Server.AdminPort)

	strEnv("DATABASE_URL", &c.Database.URL)
	strEnv("POSTGRES_USERNAME", &c.Database.Username)
	strEnv("POSTGRES_PASSWORD", &c.Database.Password)

	if v := os.Getenv("KAFKA_BROKERS"); v != "" {
		c.Kafka.Brokers = strings.Split(v, ",")
	}
	strEnv("KAFKA_TOPIC", &c.Kafka.Topic)
	strEnv("KAFKA_CONSUMER_GROUP", &c.Kafka.ConsumerGroup)

	boolEnv("TRACING_ENABLED", &c.Tracing.Enabled)
	strEnv("OTEL_EXPORTER_OTLP_ENDPOINT", &c.Tracing.OTLPEndpoint)
	if v := os.Getenv("TRACING_SAMPLE_RATIO"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			c.Tracing.SampleRatio = f
		}
	}

	boolEnv("AUTH_ENABLED", &c.Auth.Enabled)
	strEnv("AUTH_ISSUER_URL", &c.Auth.IssuerURL)
	strEnv("AUTH_AUDIENCE", &c.Auth.Audience)

	strEnv("SERVICE_KYC_URL", &c.Services.KYC)
	strEnv("SERVICE_SCORING_URL", &c.Services.Scoring)
}

// DSN assembles the pgx connection string, injecting the credentials that
// arrived through the Secret rather than the ConfigMap.
func (d Database) DSN() string {
	if d.URL == "" {
		return ""
	}
	dsn := d.URL
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	var params []string
	if d.Username != "" {
		params = append(params, "user="+d.Username)
	}
	if d.Password != "" {
		params = append(params, "password="+d.Password)
	}
	if len(params) == 0 {
		return dsn
	}
	return dsn + sep + strings.Join(params, "&")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func strEnv(key string, dst *string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func intEnv(key string, dst *int) {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}

func boolEnv(key string, dst *bool) {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			*dst = b
		}
	}
}
