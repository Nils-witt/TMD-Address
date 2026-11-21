package api

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testReadOnlyAudience = "browser-app"
	pathStreets          = "/api/streets"
	pathCities           = "/api/suggest/cities"
	audClaim             = "aud"
	expClaim             = "exp"
	issClaim             = "iss"
	trustedPeer          = "10.1.2.3:4567"
)

// readOnlyTestHandler is the auth chain of NewHandler around a handler that
// answers 200 with the principal's subject in X-Subject.
func readOnlyTestHandler(t *testing.T, proxy ProxyAuthConfig) (http.Handler, func(aud string) string) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	provider := newFakeProvider(t, key)

	oidc, err := NewOIDCVerifier(t.Context(), OIDCConfig{
		IssuerURL: provider.URL, Audience: testAudience, ReadOnlyAudiences: []string{"", testReadOnlyAudience},
	})
	if err != nil {
		t.Fatal(err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFrom(r.Context())
		w.Header().Set("X-Subject", p.Subject)
		w.WriteHeader(http.StatusOK)
	})

	sign := func(aud string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			issClaim: provider.URL, audClaim: aud, subClaim: testSubject, expClaim: time.Now().Add(time.Hour).Unix(),
		})
		token.Header["kid"] = testKID

		s, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	return proxyAuthMiddleware(proxy)(authMiddleware(oidc, fakeAPITokens{})(requireReadAccess(inner))), sign
}

func TestReadOnlyAudience(t *testing.T) {
	t.Parallel()

	handler, sign := readOnlyTestHandler(t, ProxyAuthConfig{})

	full, readOnly := sign(testAudience), sign(testReadOnlyAudience)

	tests := []struct {
		name, method, path, token string
		want                      int
	}{
		{"full token writes", http.MethodPost, pathStreets, full, http.StatusOK},
		{"full token lists tokens", http.MethodGet, "/api/tokens", full, http.StatusOK},
		{"read-only suggests", http.MethodGet, "/api/suggest/streets", readOnly, http.StatusOK},
		{"read-only reverse", http.MethodGet, "/api/suggest/reverse", readOnly, http.StatusOK},
		{"read-only lists streets", http.MethodGet, pathStreets, readOnly, http.StatusOK},
		{"read-only gets a street", http.MethodGet, "/api/streets/0190e1a0-0000-7000-8000-000000000000", readOnly, http.StatusOK},
		{"read-only health", http.MethodGet, "/healthz", readOnly, http.StatusOK},
		{"read-only HEAD", http.MethodHead, "/api/cities", readOnly, http.StatusOK},
		{"read-only writes", http.MethodPost, pathStreets, readOnly, http.StatusForbidden},
		{"read-only deletes", http.MethodDelete, "/api/geo-objects/x", readOnly, http.StatusForbidden},
		{"read-only bulk", http.MethodPost, "/api/bulk", readOnly, http.StatusForbidden},
		{"read-only tokens", http.MethodGet, "/api/tokens", readOnly, http.StatusForbidden},
		{"read-only collectors", http.MethodGet, "/api/collectors", readOnly, http.StatusForbidden},
		{"read-only path escape", http.MethodGet, "/api/streets/../tokens", readOnly, http.StatusForbidden},
		{"read-only lookalike prefix", http.MethodGet, "/api/streetsfoo", readOnly, http.StatusForbidden},
		{"other audience", http.MethodGet, "/api/suggest/streets", sign("other"), http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}
		})
	}
}

func TestProxyAuth(t *testing.T) {
	t.Parallel()

	const header, dn = "X-SSL-Client-DN", "CN=alice,O=Example"

	handler, sign := readOnlyTestHandler(t, ProxyAuthConfig{
		Header:         header,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::1/128")},
	})

	tests := []struct {
		name, method, path, remote, dn, token string
		want                                  int
		wantSubject                           string
	}{
		{"trusted proxy reads", http.MethodGet, pathCities, trustedPeer, dn, "", http.StatusOK, dn},
		{"trusted IPv6 proxy", http.MethodGet, pathCities, "[::1]:4567", dn, "", http.StatusOK, dn},
		{"trusted proxy is read-only", http.MethodPost, pathStreets, trustedPeer, dn, "", http.StatusForbidden, ""},
		{"untrusted peer", http.MethodGet, pathCities, "192.0.2.1:4567", dn, "", http.StatusUnauthorized, ""},
		{"no client named", http.MethodGet, pathCities, trustedPeer, "  ", "", http.StatusUnauthorized, ""},
		{"bearer token wins", http.MethodPost, pathStreets, trustedPeer, dn, sign(testAudience), http.StatusOK, testSubject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.path, nil)
			req.RemoteAddr = tt.remote
			req.Header.Set(header, tt.dn)

			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}

			if got := rec.Header().Get("X-Subject"); got != tt.wantSubject {
				t.Fatalf("subject = %q, want %q", got, tt.wantSubject)
			}
		})
	}
}

func TestCORSMiddleware(t *testing.T) {
	t.Parallel()

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	tests := []struct {
		name, allowed, origin, wantOrigin, wantCredentials string
	}{
		{"any origin", "*", "https://app.example", "*", ""},
		{"listed origin", "https://app.example, http://localhost:5173/", "http://localhost:5173", "http://localhost:5173", "true"},
		{"unlisted origin", "https://app.example", "https://evil.example", "", ""},
		{"no origin", "https://app.example", "", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/suggest/streets", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}

			rec := httptest.NewRecorder()
			corsMiddleware(tt.allowed)(ok).ServeHTTP(rec, req)

			if rec.Code != http.StatusNoContent {
				t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
			}

			if got := rec.Header().Get("Access-Control-Allow-Origin"); got != tt.wantOrigin {
				t.Errorf("Allow-Origin = %q, want %q", got, tt.wantOrigin)
			}

			if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != tt.wantCredentials {
				t.Errorf("Allow-Credentials = %q, want %q", got, tt.wantCredentials)
			}

			if got := rec.Header().Get("Vary"); got != "Origin" {
				t.Errorf("Vary = %q, want Origin", got)
			}
		})
	}
}
