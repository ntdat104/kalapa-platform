package obs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics sở hữu registry Prometheus của service. Dùng registry riêng (thay vì
// prometheus.DefaultRegisterer) giúp /metrics không dính những gì một
// dependency nào đó đã đăng ký toàn cục, nhờ vậy cardinality của lần scrape là
// thứ repo này thực sự kiểm soát được.
type Metrics struct {
	Registry *prometheus.Registry

	requests  *prometheus.CounterVec
	duration  *prometheus.HistogramVec
	inflight  prometheus.Gauge
	events    *prometheus.CounterVec
	buildInfo *prometheus.GaugeVec
}

func NewMetrics(service, version string) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		Registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_server_requests_total",
			Help: "HTTP requests handled, by route and status class.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_server_request_duration_seconds",
			Help: "HTTP request latency in seconds.",
			// Native histogram thì hay hơn nhưng cần bật cờ ở Prometheus; các
			// bucket này khớp với panel SLO trong dashboard Grafana.
			Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"method", "route"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_server_requests_in_flight",
			Help: "In-flight HTTP requests.",
		}),
		events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "kalapa_events_total",
			Help: "Domain events produced or consumed, by topic and outcome.",
		}, []string{"topic", "direction", "outcome"}),
		buildInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "kalapa_build_info",
			Help: "Always 1; the labels carry the build identity.",
		}, []string{"service", "version"}),
	}

	reg.MustRegister(
		m.requests, m.duration, m.inflight, m.events, m.buildInfo,
		// Panel "pod này sắp bị OOMKill chưa" đọc từ collector của Go runtime và
		// của process; với limit 64Mi thì panel đó rất đáng quan tâm.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.buildInfo.WithLabelValues(service, version).Set(1)
	return m
}

// Handler chỉ được gắn vào port quản trị.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// RecordEvent đếm traffic Kafka. direction là "produce" hoặc "consume".
func (m *Metrics) RecordEvent(topic, direction, outcome string) {
	m.events.WithLabelValues(topic, direction, outcome).Inc()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// routeResolver được *http.ServeMux thoả mãn. Nó cho middleware biết pattern
// đã đăng ký của một request TRƯỚC khi dispatch — cách duy nhất đáng tin để có
// label `route` hữu hạn: r.Pattern chỉ được gán trên request mà mux truyền
// xuống dưới, không phải trên request mà middleware bên ngoài đang giữ.
type routeResolver interface {
	Handler(*http.Request) (http.Handler, string)
}

// Middleware gắn đo đạc vào một handler. Label `route` là pattern đã đăng ký,
// không bao giờ là đường dẫn thô — dùng đường dẫn sẽ làm cardinality vô hạn
// ngay khi có ai đó gọi /kyc/applications/<uuid>.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	resolver, _ := next.(routeResolver)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := "unmatched"
		if resolver != nil {
			if _, pattern := resolver.Handler(r); pattern != "" {
				route = pattern
			}
		}

		start := time.Now()
		m.inflight.Inc()
		defer m.inflight.Dec()

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		m.requests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.duration.WithLabelValues(r.Method, route).Observe(time.Since(start).Seconds())
	})
}

// Route mở cùng phép tra cứu đó cho middleware khác (phần tracing dùng nó để
// đặt tên span là `GET /kyc/applications/{id}` thay vì mỗi id một tên span).
func Route(h http.Handler, r *http.Request) string {
	if resolver, ok := h.(routeResolver); ok {
		if _, pattern := resolver.Handler(r); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}
