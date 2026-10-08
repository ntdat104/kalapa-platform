// Package httpx runs the two listeners every Kalapa service exposes and owns
// the shutdown choreography the Deployment relies on.
//
// Why two listeners:
//   - :8080 business traffic, reachable through the Service and the Ingress
//   - :9090 /healthz/live, /healthz/ready, /metrics — reachable only from
//     kubelet and Prometheus, never from the Ingress
//
// This is the Go equivalent of YAS's server.port + management.server.port
// split. Keeping probes off the public port means a saturated request queue
// cannot starve the liveness probe and get the pod killed while it is merely
// busy.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/config"
	"github.com/kalapa-lab/kalapa-platform/internal/platform/obs"
)

// Checker reports whether a dependency is usable right now.
type Checker func(context.Context) error

// Server ties the app mux, the admin mux and the lifecycle together.
type Server struct {
	cfg     config.Server
	log     *slog.Logger
	metrics *obs.Metrics

	ready atomic.Bool
	live  atomic.Bool

	readinessChecks map[string]Checker
	backgroundTasks []func(context.Context) error
}

func New(cfg config.Server, log *slog.Logger, metrics *obs.Metrics) *Server {
	s := &Server{
		cfg:             cfg,
		log:             log,
		metrics:         metrics,
		readinessChecks: map[string]Checker{},
	}
	// Liveness is true from construction: "the process is not wedged".
	// Readiness stays false until Run has wired everything up.
	s.live.Store(true)
	return s
}

// AddReadinessCheck registers a dependency probe. A failing check removes the
// pod from the Service endpoints without restarting it — the correct response
// to "Postgres is briefly unreachable".
func (s *Server) AddReadinessCheck(name string, c Checker) {
	s.readinessChecks[name] = c
}

// Go registers a goroutine (a Kafka consumer loop, say) whose lifetime is tied
// to the server's. Returning an error from it shuts the whole process down.
func (s *Server) Go(fn func(context.Context) error) {
	s.backgroundTasks = append(s.backgroundTasks, fn)
}

func (s *Server) adminMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz/live", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately dependency-free. If liveness checked Postgres, a
		// database blip would restart every pod at once and turn a small
		// outage into a thundering-herd reconnect storm.
		if s.live.Load() {
			writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "DOWN"})
	})

	mux.HandleFunc("GET /healthz/ready", func(w http.ResponseWriter, r *http.Request) {
		if !s.ready.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "OUT_OF_SERVICE",
				"reason": "starting or draining",
			})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		details := map[string]string{}
		status := http.StatusOK
		for name, check := range s.readinessChecks {
			if err := check(ctx); err != nil {
				details[name] = "DOWN: " + err.Error()
				status = http.StatusServiceUnavailable
				continue
			}
			details[name] = "UP"
		}
		body := map[string]any{"status": "UP", "checks": details}
		if status != http.StatusOK {
			body["status"] = "DOWN"
		}
		writeJSON(w, status, body)
	})

	mux.Handle("GET /metrics", s.metrics.Handler())
	return mux
}

// Run starts both listeners and blocks until SIGTERM, then performs the drain
// sequence described in docs/02-kubernetes-deep-dive.md.
func (s *Server) Run(parent context.Context, appHandler http.Handler, serviceName string) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// otelhttp extracts the inbound traceparent and starts the server span.
	// Naming the span after the registered route keeps Tempo's span-name
	// cardinality finite, the same reason the metrics label does it.
	traced := otelhttp.NewHandler(appHandler, serviceName,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			// A Go 1.22 ServeMux pattern already starts with the method
			// ("GET /kyc/applications/{id}"), so prefixing r.Method again
			// yields "GET GET /kyc/...". Fall back to the method alone when
			// nothing matched, which keeps the name bounded either way.
			if route := obs.Route(appHandler, r); route != "unmatched" {
				return route
			}
			return r.Method + " unmatched"
		}),
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/healthz" }),
	)

	appSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.HTTPPort),
		Handler:           s.metrics.Middleware(traced),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       s.cfg.ReadTimeout,
		WriteTimeout:      s.cfg.WriteTimeout,
		BaseContext:       func(net.Listener) context.Context { return parent },
	}
	adminSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", s.cfg.AdminPort),
		Handler:           s.adminMux(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 2+len(s.backgroundTasks))
	go serve(appSrv, "app", s.log, errCh)
	go serve(adminSrv, "admin", s.log, errCh)
	for _, task := range s.backgroundTasks {
		go func(fn func(context.Context) error) {
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- err
			}
		}(task)
	}

	s.ready.Store(true)
	s.log.Info("service started",
		slog.Int("http_port", s.cfg.HTTPPort),
		slog.Int("admin_port", s.cfg.AdminPort))

	var runErr error
	select {
	case <-ctx.Done():
		s.log.Info("shutdown signal received, draining")
	case runErr = <-errCh:
		s.log.Error("fatal error, draining", slog.Any("error", runErr))
	}

	// Step 1: fail readiness immediately. The endpoints controller now starts
	// removing this pod from the Service. The container's preStop hook is
	// sleeping in parallel, which is what buys kube-proxy/the Ingress time to
	// notice before we stop accepting.
	s.ready.Store(false)

	// Step 2: stop accepting, let in-flight requests finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := appSrv.Shutdown(shutdownCtx); err != nil {
		s.log.Warn("app listener did not drain cleanly", slog.Any("error", err))
	}
	// Step 3: admin last, so Prometheus can scrape the final counter values
	// and the kubelet keeps getting a truthful readiness answer until the end.
	_ = adminSrv.Shutdown(shutdownCtx)

	s.log.Info("shutdown complete")
	return runErr
}

func serve(srv *http.Server, name string, log *slog.Logger, errCh chan<- error) {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- fmt.Errorf("%s listener: %w", name, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// WriteJSON is the shared response helper for the domain handlers.
func WriteJSON(w http.ResponseWriter, status int, body any) { writeJSON(w, status, body) }

// WriteError emits a consistent error envelope so the gateway can pass upstream
// failures through without reshaping them.
func WriteError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
