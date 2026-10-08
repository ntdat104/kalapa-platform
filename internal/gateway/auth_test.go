package gateway

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestIssuer stands up a fake Keycloak that serves a JWKS for a key we
// control, so the whole verification path is exercised without a cluster.
func newTestIssuer(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/protocol/openid-connect/certs", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "test-key",
			"kty": "RSA",
			"alg": "RS256",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, key
}

func sign(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"}) + "." + enc(claims)
	h := crypto.SHA256.New()
	h.Write([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h.Sum(nil))
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestVerify(t *testing.T) {
	issuer, key := newTestIssuer(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	log := slog.New(slog.DiscardHandler)
	auth := NewAuthenticator(true, issuer.URL, "kalapa-api", log)

	valid := map[string]any{
		"iss": issuer.URL,
		"sub": "user-1",
		"aud": []string{"kalapa-api", "account"},
		"exp": time.Now().Add(time.Hour).Unix(),
	}

	t.Run("accepts a well formed token", func(t *testing.T) {
		claims, err := auth.Verify(context.Background(), sign(t, key, "test-key", "RS256", valid))
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if claims.Subject != "user-1" {
			t.Errorf("subject = %q", claims.Subject)
		}
	})

	t.Run("rejects a token signed by another key", func(t *testing.T) {
		if _, err := auth.Verify(context.Background(), sign(t, other, "test-key", "RS256", valid)); err == nil {
			t.Fatal("expected signature failure")
		}
	})

	t.Run("rejects an unexpected algorithm", func(t *testing.T) {
		// alg:none and HS256 key-confusion both rely on the verifier trusting
		// the header. This asserts we never do.
		if _, err := auth.Verify(context.Background(), sign(t, key, "test-key", "none", valid)); err == nil {
			t.Fatal("expected alg rejection")
		}
	})

	t.Run("rejects a foreign issuer", func(t *testing.T) {
		claims := map[string]any{}
		for k, v := range valid {
			claims[k] = v
		}
		claims["iss"] = "http://evil.example.com/realms/kalapa"
		_, err := auth.Verify(context.Background(), sign(t, key, "test-key", "RS256", claims))
		if err == nil || !strings.Contains(err.Error(), "issuer mismatch") {
			t.Fatalf("expected issuer mismatch, got %v", err)
		}
	})

	t.Run("rejects an expired token", func(t *testing.T) {
		claims := map[string]any{}
		for k, v := range valid {
			claims[k] = v
		}
		claims["exp"] = time.Now().Add(-2 * time.Hour).Unix()
		if _, err := auth.Verify(context.Background(), sign(t, key, "test-key", "RS256", claims)); err == nil {
			t.Fatal("expected expiry rejection")
		}
	})

	t.Run("rejects a wrong audience", func(t *testing.T) {
		claims := map[string]any{}
		for k, v := range valid {
			claims[k] = v
		}
		claims["aud"] = "some-other-client"
		if _, err := auth.Verify(context.Background(), sign(t, key, "test-key", "RS256", claims)); err == nil {
			t.Fatal("expected audience rejection")
		}
	})

	t.Run("rejects a malformed token", func(t *testing.T) {
		if _, err := auth.Verify(context.Background(), "not.a.jwt.at.all"); err == nil {
			t.Fatal("expected parse failure")
		}
	})
}

func TestMiddlewareRequiresBearer(t *testing.T) {
	issuer, _ := newTestIssuer(t)
	auth := NewAuthenticator(true, issuer.URL, "", slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	handler := auth.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run without a token")
	}))
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kyc/applications", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Error("401 must carry a WWW-Authenticate challenge")
	}
}
