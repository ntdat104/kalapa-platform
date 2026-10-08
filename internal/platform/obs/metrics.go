package obs

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns the service's Prometheus registry. A private registry (rather
// than prometheus.DefaultRegisterer) keeps /metrics free of whatever a
// dependency decided to register globally, so the cardinality of the scrape is
// something this repo actually controls.
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
			// Native histograms would be nicer but need a Prometheus flag;
			// these buckets match the SLO panels in the Grafana dashboard.
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
		// Go runtime + process collectors are what the "is this pod about to be
		// OOMKilled" panel reads; on a 64Mi limit that panel matters.
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m.buildInfo.WithLabelValues(service, version).Set(1)
	return m
}

// Handler is mounted on the admin port only.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}

// RecordEvent tracks Kafka traffic. direction is "produce" or "consume".
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

// routeResolver is satisfied by *http.ServeMux. It lets the middleware learn
// the registered pattern for a request *before* dispatching, which is the only
// reliable way to get a bounded `route` label: r.Pattern is populated on the
// request the mux passes downstream, not on the one outer middleware holds.
type routeResolver interface {
	Handler(*http.Request) (http.Handler, string)
}

// Middleware instruments a handler. The `route` label is the registered
// pattern, never the raw path — using the path would make cardinality unbounded
// the moment someone requests /kyc/applications/<uuid>.
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

// Route exposes the same lookup to other middleware (tracing uses it to name
// spans `GET /kyc/applications/{id}` instead of one span name per id).
func Route(h http.Handler, r *http.Request) string {
	if resolver, ok := h.(routeResolver); ok {
		if _, pattern := resolver.Handler(r); pattern != "" {
			return pattern
		}
	}
	return "unmatched"
}
