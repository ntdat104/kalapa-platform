// Package config nạp cấu hình của service theo đúng cách phân tầng mà YAS
// dùng cho Spring Boot:
//
//  1. giá trị mặc định (biên dịch sẵn trong code)
//  2. một file YAML mount từ ConfigMap    (CONFIG_FILE, mặc định /etc/kalapa/config.yaml)
//  3. một lớp phủ YAML riêng từng service (CONFIG_EXTRA_FILES, ngăn cách bằng dấu phẩy)
//  4. biến môi trường / Secret            (thắng tất cả)
//
// Tầng 2 và 3 tương ứng SPRING_CONFIG_ADDITIONAL_LOCATION; tầng 4 tương ứng
// envFrom: secretRef. Việc chỉ để thông tin đăng nhập ở tầng 4 là có chủ ý —
// nội dung ConfigMap ai trong namespace cũng đọc được, Secret thì không.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config gom mọi tham số mà bất kỳ service Kalapa nào hiểu được. Service bỏ
// qua những phần nó không dùng; dùng chung một struct giúp ConfigMap
// kalapa-config giống hệt nhau ở mọi service.
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
	// HTTPPort phục vụ traffic nghiệp vụ và là port duy nhất nằm sau cổng `http`
	// của Service. AdminPort phục vụ /healthz/* và /metrics, không bao giờ lộ ra
	// qua Ingress — đúng kiểu tách server.port và management.server.port của YAS.
	HTTPPort        int           `yaml:"httpPort"`
	AdminPort       int           `yaml:"adminPort"`
	ReadTimeout     time.Duration `yaml:"readTimeout"`
	WriteTimeout    time.Duration `yaml:"writeTimeout"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout"`
}

type Database struct {
	// URL là chuỗi DSN kiểu libpq/pgx. Username và Password được tiêm riêng để
	// nửa không nhạy cảm nằm được trong ConfigMap, còn thông tin đăng nhập thì
	// chỉ đến từ Secret.
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
	// Enabled bật/tắt việc verify JWT ở gateway. Mặc định tắt để hệ thống dùng
	// được ngay cả khi Keycloak chưa khởi động xong lần đầu.
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

// Load giải quyết cả bốn tầng. serviceName là giá trị khởi đầu cho Service.Name
// khi cả YAML lẫn SERVICE_NAME đều không cung cấp.
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
			// File không tồn tại không phải lỗi: `go run ./cmd/kyc` trên laptop
			// không có ConfigMap nào, và lớp phủ tuỳ chọn thì đúng nghĩa là tuỳ chọn.
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

// DSN ghép chuỗi kết nối cho pgx, chèn thông tin đăng nhập vốn đến từ Secret
// chứ không phải từ ConfigMap.
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
