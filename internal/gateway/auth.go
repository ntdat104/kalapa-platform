package gateway

import (
	"context"
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kalapa-lab/kalapa-platform/internal/platform/httpx"
)

// Authenticator verifies Keycloak-issued RS256 access tokens against the
// realm's JWKS.
//
// It is written against the standard library on purpose. An OIDC library would
// be three lines, but then the three things that actually break in a cluster —
// the issuer URL differing between browser and pod, key rotation, and clock
// skew — stay invisible. All three are handled explicitly below.
type Authenticator struct {
	Enabled   bool
	IssuerURL string // what the token's `iss` claim must equal
	JWKSURL   string
	Audience  string
	Log       *slog.Logger

	mu       sync.RWMutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	cacheTTL time.Duration
	client   *http.Client
}

func NewAuthenticator(enabled bool, issuerURL, audience string, log *slog.Logger) *Authenticator {
	issuerURL = strings.TrimSuffix(issuerURL, "/")
	return &Authenticator{
		Enabled:   enabled,
		IssuerURL: issuerURL,
		JWKSURL:   issuerURL + "/protocol/openid-connect/certs",
		Audience:  audience,
		Log:       log,
		keys:      map[string]*rsa.PublicKey{},
		cacheTTL:  10 * time.Minute,
		client:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" || raw == r.Header.Get("Authorization") {
			w.Header().Set("WWW-Authenticate", `Bearer realm="kalapa"`)
			httpx.WriteError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := a.Verify(r.Context(), raw)
		if err != nil {
			a.Log.WarnContext(r.Context(), "token rejected", slog.Any("error", err))
			httpx.WriteError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		// Hand the subject downstream so the KYC service can attribute the
		// application without re-verifying the token. Internal services trust
		// this header precisely because a NetworkPolicy makes the gateway the
		// only pod allowed to reach them.
		r.Header.Set("X-Kalapa-Subject", claims.Subject)
		next.ServeHTTP(w, r)
	})
}

type Claims struct {
	Issuer   string `json:"iss"`
	Subject  string `json:"sub"`
	Audience any    `json:"aud"` // string or []string, per RFC 7519
	Expiry   int64  `json:"exp"`
	IssuedAt int64  `json:"iat"`
}

func (a *Authenticator) Verify(ctx context.Context, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT")
	}

	headerJSON, err := b64(parts[0])
	if err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, fmt.Errorf("header json: %w", err)
	}
	// Pinning the algorithm closes the `alg: none` and HS256-key-confusion
	// families of attack in one line.
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unexpected alg %q", header.Alg)
	}

	key, err := a.keyFor(ctx, header.Kid)
	if err != nil {
		return nil, err
	}

	sig, err := b64(parts[2])
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	signed := parts[0] + "." + parts[1]
	hashed := crypto.SHA256.New()
	hashed.Write([]byte(signed))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed.Sum(nil), sig); err != nil {
		return nil, fmt.Errorf("signature invalid: %w", err)
	}

	payload, err := b64(parts[1])
	if err != nil {
		return nil, fmt.Errorf("payload: %w", err)
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("payload json: %w", err)
	}

	now := time.Now()
	// 60s leeway: pods and the host clock drift, and a strict comparison makes
	// tokens fail intermittently in a way that looks like a networking bug.
	if claims.Expiry > 0 && now.After(time.Unix(claims.Expiry, 0).Add(60*time.Second)) {
		return nil, fmt.Errorf("token expired")
	}
	if a.IssuerURL != "" && strings.TrimSuffix(claims.Issuer, "/") != a.IssuerURL {
		// This is the error you will hit first. Keycloak stamps `iss` with the
		// URL the *browser* used; a pod validating against the in-cluster
		// Service name will see a mismatch. Fix it by configuring Keycloak's
		// hostname, not by relaxing this check.
		return nil, fmt.Errorf("issuer mismatch: token=%q expected=%q", claims.Issuer, a.IssuerURL)
	}
	if a.Audience != "" && !claims.hasAudience(a.Audience) {
		return nil, fmt.Errorf("audience mismatch")
	}
	return &claims, nil
}

func (c Claims) hasAudience(want string) bool {
	switch v := c.Audience.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

// keyFor returns the signing key, refreshing the JWKS at most once per cache
// TTL — and immediately on an unknown kid, which is how key rotation is
// supposed to be handled.
func (a *Authenticator) keyFor(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	a.mu.RLock()
	key, ok := a.keys[kid]
	fresh := time.Since(a.fetched) < a.cacheTTL
	a.mu.RUnlock()
	if ok && fresh {
		return key, nil
	}

	if err := a.refresh(ctx); err != nil {
		if ok {
			// Serve the cached key rather than reject every request while
			// Keycloak restarts.
			return key, nil
		}
		return nil, err
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	if key, ok := a.keys[kid]; ok {
		return key, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

func (a *Authenticator) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.JWKSURL, nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks returned %d", resp.StatusCode)
	}

	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := b64(k.N)
		if err != nil {
			continue
		}
		eBytes, err := b64(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("jwks contained no RSA keys")
	}

	a.mu.Lock()
	a.keys = keys
	a.fetched = time.Now()
	a.mu.Unlock()
	return nil
}

// JWT uses base64url without padding (RFC 7515 §2).
func b64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
