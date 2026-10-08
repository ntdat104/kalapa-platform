// Package gateway là service duy nhất mà Ingress định tuyến tới. Nó đóng vai
// trò mà YAS giao cho storefront-bff: kết thúc hợp đồng công khai, verify token
// một lần, rồi toả ra gọi các service nội bộ qua DNS của cluster.
//
// Mọi việc nó làm đều có thể do Ingress controller hoặc một service mesh đảm
// nhiệm. Nó tồn tại dưới dạng một process Go để trace có một chặng đầu nhìn
// thấy được, và để endpoint tổng hợp minh hoạ được span kiểu toả nhánh — hai
// span con song song dưới một span cha là hình dạng bạn cần nhận ra trong Tempo.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
)

type Service struct {
	KYCURL     string
	ScoringURL string
	Tracer     trace.Tracer
	Log        *slog.Logger
	Auth       *Authenticator

	client *http.Client
	once   sync.Once
}

// httpClient được tạo lười để Service ở giá trị zero vẫn dùng được trong test.
// otelhttp.NewTransport chính là thứ chèn `traceparent` vào request đi ra —
// thiếu nó, service phía dưới sẽ mở một trace hoàn toàn mới.
func (s *Service) httpClient() *http.Client {
	s.once.Do(func() {
		s.client = &http.Client{
			Timeout: 10 * time.Second,
			Transport: otelhttp.NewTransport(&http.Transport{
				MaxIdleConns:        32,
				MaxIdleConnsPerHost: 8,
				IdleConnTimeout:     60 * time.Second,
			}),
		}
	})
	return s.client
}

func (s *Service) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Hợp đồng công khai: /api/** . Tiền tố /api bị cắt bỏ trước khi chuyển
	// tiếp, đúng phép viết lại mà YAS diễn đạt bằng
	// `RewritePath=/api/(?<segment>.*), /$\{segment}`.
	mux.Handle("POST /api/kyc/applications", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/kyc/applications", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/kyc/applications/{id}", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/scoring/scores", s.protect(s.proxy(func() string { return s.ScoringURL })))
	mux.Handle("GET /api/scoring/scores/{applicationId}", s.protect(s.proxy(func() string { return s.ScoringURL })))

	// Endpoint tổng hợp: một lời gọi từ client, hai lời gọi upstream song song.
	mux.Handle("GET /api/applications/{id}", s.protect(http.HandlerFunc(s.aggregate)))

	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"service": "gateway", "status": "ok"})
	})
	return mux
}

// protect áp dụng việc verify JWT khi xác thực được bật.
func (s *Service) protect(next http.Handler) http.Handler {
	if s.Auth == nil || !s.Auth.Enabled {
		return next
	}
	return s.Auth.Middleware(next)
}

// proxy chuyển tiếp request tới `base` và cắt bỏ tiền tố /api. target là một
// hàm để URL được đọc tại thời điểm xử lý request — nhờ vậy một thay đổi cấu
// hình do Reloader kích hoạt có hiệu lực ngay, không bị một chỗ nào đó cache lại.
func (s *Service) proxy(target func() string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := strings.TrimSuffix(target(), "/")
		if base == "" {
			httpx.WriteError(w, http.StatusBadGateway, "upstream not configured")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api")
		upstream := base + path
		if r.URL.RawQuery != "" {
			upstream += "?" + r.URL.RawQuery
		}

		ctx, span := s.Tracer.Start(r.Context(), "gateway.proxy")
		defer span.End()

		req, err := http.NewRequestWithContext(ctx, r.Method, upstream, r.Body)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "bad upstream request")
			return
		}
		copyHeader(req.Header, r.Header, "Content-Type", "Accept", "Authorization")

		resp, err := s.httpClient().Do(req)
		if err != nil {
			s.Log.ErrorContext(ctx, "upstream call failed",
				slog.String("upstream", upstream), slog.Any("error", err))
			httpx.WriteError(w, http.StatusBadGateway, "upstream unavailable")
			return
		}
		defer resp.Body.Close()

		for _, h := range []string{"Content-Type"} {
			if v := resp.Header.Get(h); v != "" {
				w.Header().Set(h, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		// Sao chép có giới hạn: một upstream phát dữ liệu vô tận không được phép
		// chiếm giữ bộ nhớ của gateway.
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	})
}

type aggregateResponse struct {
	Application json.RawMessage `json:"application,omitempty"`
	Score       json.RawMessage `json:"score,omitempty"`
	Pending     bool            `json:"scoring_pending"`
}

// aggregate lấy hồ sơ và điểm của nó đồng thời. Trong Tempo, việc đó hiện ra
// thành hai span anh em chồng lấn thời gian dưới span của gateway — nếu chúng
// hiện ra nối tiếp nhau thì tính song song đã hỏng.
func (s *Service) aggregate(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.Tracer.Start(r.Context(), "gateway.aggregate")
	defer span.End()

	id := r.PathValue("id")
	auth := r.Header.Get("Authorization")

	var (
		wg               sync.WaitGroup
		appBody, scoBody json.RawMessage
		appErr           error
		scoStatus        int
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		appBody, _, appErr = s.fetch(ctx, fmt.Sprintf("%s/kyc/applications/%s", strings.TrimSuffix(s.KYCURL, "/"), id), auth)
	}()
	go func() {
		defer wg.Done()
		scoBody, scoStatus, _ = s.fetch(ctx, fmt.Sprintf("%s/scoring/scores/%s", strings.TrimSuffix(s.ScoringURL, "/"), id), auth)
	}()
	wg.Wait()

	if appErr != nil {
		httpx.WriteError(w, http.StatusBadGateway, "kyc service unavailable")
		return
	}

	out := aggregateResponse{Application: appBody}
	if scoStatus == http.StatusOK {
		out.Score = scoBody
	} else {
		// Không phải lỗi: sự kiện có thể vẫn đang trên đường. Phơi nó ra dưới
		// dạng một cờ thay vì trả 404 chính là điều làm cho tính nhất quán cuối
		// cùng dùng được từ phía người gọi.
		out.Pending = true
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) fetch(ctx context.Context, url, auth string) (json.RawMessage, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func copyHeader(dst, src http.Header, keys ...string) {
	for _, k := range keys {
		if v := src.Get(k); v != "" {
			dst.Set(k, v)
		}
	}
}
