// Package httpx chạy hai listener mà mọi service Kalapa phơi ra, và nắm toàn
// bộ trình tự tắt mềm mà Deployment dựa vào.
//
// Vì sao hai listener:
//   - :8080 traffic nghiệp vụ, tới được qua Service và Ingress
//   - :9090 /healthz/live, /healthz/ready, /metrics — chỉ kubelet và
//     Prometheus tới được, không bao giờ qua Ingress
//
// Đây là bản Go của việc YAS tách server.port và management.server.port. Giữ
// probe ra khỏi port công khai nghĩa là một hàng đợi request bị bão hoà không
// thể bỏ đói liveness probe và làm pod bị giết trong khi nó chỉ đang bận.
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

// Checker cho biết một phụ thuộc có dùng được ngay lúc này không.
type Checker func(context.Context) error

// Server gắn mux nghiệp vụ, mux quản trị và vòng đời lại với nhau.
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
	// Liveness đúng ngay từ lúc khởi tạo: "process chưa bị treo cứng".
	// Readiness vẫn false cho tới khi Run nối xong mọi thứ.
	s.live.Store(true)
	return s
}

// AddReadinessCheck đăng ký một phép kiểm tra phụ thuộc. Kiểm tra hỏng sẽ gỡ
// pod khỏi Service endpoints mà KHÔNG restart nó — đó là phản ứng đúng cho
// tình huống "Postgres tạm thời không với tới được".
func (s *Server) AddReadinessCheck(name string, c Checker) {
	s.readinessChecks[name] = c
}

// Go đăng ký một goroutine (ví dụ vòng lặp consumer Kafka) có vòng đời gắn với
// vòng đời của server. Nó trả về lỗi thì cả process tắt.
func (s *Server) Go(fn func(context.Context) error) {
	s.backgroundTasks = append(s.backgroundTasks, fn)
}

func (s *Server) adminMux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz/live", func(w http.ResponseWriter, r *http.Request) {
		// Cố ý không phụ thuộc gì. Nếu liveness kiểm tra Postgres, một cú chập
		// database sẽ restart mọi pod cùng lúc, biến một sự cố nhỏ thành cơn
		// bão kết nối lại đồng loạt.
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

// Run khởi động cả hai listener và chặn cho tới khi nhận SIGTERM, sau đó thực
// hiện trình tự xả tải mô tả ở docs/02-kubernetes-deep-dive.md.
func (s *Server) Run(parent context.Context, appHandler http.Handler, serviceName string) error {
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// otelhttp đọc traceparent từ request đến và bắt đầu span phía server.
	// Đặt tên span theo route đã đăng ký giữ cho số tên span trong Tempo là hữu
	// hạn — cùng lý do với label của metric.
	traced := otelhttp.NewHandler(appHandler, serviceName,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			// Pattern của ServeMux từ Go 1.22 đã bắt đầu bằng method
			// ("GET /kyc/applications/{id}"), nên thêm r.Method vào trước nữa
			// sẽ ra "GET GET /kyc/...". Khi không khớp pattern nào thì chỉ dùng
			// method, để tên span vẫn luôn hữu hạn.
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

	// Bước 1: báo không sẵn sàng ngay lập tức. Endpoints controller bắt đầu gỡ
	// pod này khỏi Service. Song song đó, preStop hook của container đang ngủ —
	// chính khoảng ngủ đó mua thời gian cho kube-proxy/Ingress kịp nhận ra
	// trước khi ta ngừng nhận kết nối.
	s.ready.Store(false)

	// Bước 2: ngừng nhận kết nối mới, để các request đang chạy hoàn tất.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := appSrv.Shutdown(shutdownCtx); err != nil {
		s.log.Warn("app listener did not drain cleanly", slog.Any("error", err))
	}
	// Bước 3: đóng admin sau cùng, để Prometheus kịp scrape giá trị counter
	// cuối cùng và kubelet vẫn nhận được câu trả lời readiness trung thực tới
	// phút chót.
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

// WriteJSON là hàm trả lời dùng chung cho các handler nghiệp vụ.
func WriteJSON(w http.ResponseWriter, status int, body any) { writeJSON(w, status, body) }

// WriteError trả về một khuôn lỗi thống nhất, để gateway chuyển tiếp lỗi từ
// upstream mà không phải định dạng lại.
func WriteError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
