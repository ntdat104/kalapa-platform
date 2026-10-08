// Package gateway is the only service the Ingress routes to. It plays the role
// YAS gives to storefront-bff: terminate the public contract, verify the token
// once, and fan out to internal services over cluster DNS.
//
// Everything it does could be done by the Ingress controller or a service
// mesh. It exists as a Go process so the trace has a visible first hop and so
// the aggregate endpoint can demonstrate a fan-out span — two parallel
// children under one parent is the shape you want to recognise in Tempo.
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

// httpClient is created lazily so the zero-value Service is still usable in
// tests. otelhttp.NewTransport is what injects `traceparent` on the way out —
// without it the downstream service starts a brand-new trace.
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

	// Public contract: /api/** . The /api prefix is stripped before the
	// request is forwarded, the same rewrite YAS expresses as
	// `RewritePath=/api/(?<segment>.*), /$\{segment}`.
	mux.Handle("POST /api/kyc/applications", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/kyc/applications", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/kyc/applications/{id}", s.protect(s.proxy(func() string { return s.KYCURL })))
	mux.Handle("GET /api/scoring/scores", s.protect(s.proxy(func() string { return s.ScoringURL })))
	mux.Handle("GET /api/scoring/scores/{applicationId}", s.protect(s.proxy(func() string { return s.ScoringURL })))

	// The aggregate: one client call, two upstream calls in parallel.
	mux.Handle("GET /api/applications/{id}", s.protect(http.HandlerFunc(s.aggregate)))

	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"service": "gateway", "status": "ok"})
	})
	return mux
}

// protect applies JWT verification when auth is enabled.
func (s *Service) protect(next http.Handler) http.Handler {
	if s.Auth == nil || !s.Auth.Enabled {
		return next
	}
	return s.Auth.Middleware(next)
}

// proxy forwards the request to `base`, stripping the /api prefix. target is a
// func so the URL is read at request time, which is what makes a Reloader-
// triggered config change take effect without a code path that caches it.
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
		// Bounded copy: an upstream that streams forever must not be able to
		// pin the gateway's memory.
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	})
}

type aggregateResponse struct {
	Application json.RawMessage `json:"application,omitempty"`
	Score       json.RawMessage `json:"score,omitempty"`
	Pending     bool            `json:"scoring_pending"`
}

// aggregate fetches the application and its score concurrently. In Tempo this
// renders as two sibling spans overlapping in time under the gateway span —
// if they appear sequentially, the concurrency is broken.
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
		// Not an error: the event may still be in flight. Surfacing it as a
		// flag rather than a 404 is what makes eventual consistency usable
		// by a caller.
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
