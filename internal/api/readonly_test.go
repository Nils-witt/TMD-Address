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

// testSigner signs a token claiming issuer iss for aud with key.
type testSigner func(iss string, key *rsa.PrivateKey, aud string) string

// readOnlyTestIssuers are the providers of readOnlyTestHandler: main is
// OIDC_ISSUER_URL, readOnly one of OIDC_READONLY_ISSUER_URLS, each with its
// own key as in Authentik.
type readOnlyTestIssuers struct {
	main, readOnly       string
	mainKey, readOnlyKey *rsa.PrivateKey
	sign                 testSigner
}

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	return key
}

// readOnlyTestHandler is the auth chain of NewHandler around a handler that
// answers 200 with the principal's subject in X-Subject.
func readOnlyTestHandler(t *testing.T, proxy ProxyAuthConfig) (http.Handler, readOnlyTestIssuers) {
	t.Helper()

	iss := readOnlyTestIssuers{mainKey: newTestKey(t), readOnlyKey: newTestKey(t)}
	iss.main = newFakeProvider(t, iss.mainKey).URL
	iss.readOnly = newFakeProvider(t, iss.readOnlyKey).URL

	oidc, err := NewOIDCVerifier(t.Context(), OIDCConfig{
		IssuerURL: iss.main, Audience: testAudience, ReadOnlyIssuerURLs: []string{"", iss.readOnly},
	})
	if err != nil {
		t.Fatal(err)
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFrom(r.Context())
		w.Header().Set("X-Subject", p.Subject)
		w.WriteHeader(http.StatusOK)
	})

	iss.sign = func(issuer string, key *rsa.PrivateKey, aud string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			issClaim: issuer, audClaim: aud, subClaim: testSubject, expClaim: time.Now().Add(time.Hour).Unix(),
		})
		token.Header["kid"] = testKID

		s, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}

		return s
	}

	return proxyAuthMiddleware(proxy)(authMiddleware(oidc, fakeAPITokens{}, nil)(requireReadAccess(inner))), iss
}

func TestReadOnlyIssuer(t *testing.T) {
	t.Parallel()

	handler, iss := readOnlyTestHandler(t, ProxyAuthConfig{})

	full := iss.sign(iss.main, iss.mainKey, testAudience)
	readOnly := iss.sign(iss.readOnly, iss.readOnlyKey, testReadOnlyAudience)
	// A read-only issuer's token for this API's audience stays read-only:
	// the issuer decides, not the audience.
	readOnlyForAPI := iss.sign(iss.readOnly, iss.readOnlyKey, testAudience)

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
		{"read-only issuer with api audience writes", http.MethodPost, pathStreets, readOnlyForAPI, http.StatusForbidden},
		{"read-only issuer with api audience reads", http.MethodGet, pathStreets, readOnlyForAPI, http.StatusOK},
		{"main issuer other audience", http.MethodGet, "/api/suggest/streets", iss.sign(iss.main, iss.mainKey, "other"), http.StatusUnauthorized},
		{"main issuer signed by read-only key", http.MethodPost, pathStreets, iss.sign(iss.main, iss.readOnlyKey, testAudience), http.StatusUnauthorized},
		{"read-only issuer signed by main key", http.MethodGet, pathStreets, iss.sign(iss.readOnly, iss.mainKey, testReadOnlyAudience), http.StatusUnauthorized},
		{"unknown issuer", http.MethodGet, pathStreets, iss.sign("https://evil.example", iss.mainKey, testAudience), http.StatusUnauthorized},
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

func TestNewOIDCVerifierDuplicateReadOnlyIssuer(t *testing.T) {
	t.Parallel()

	main := newFakeProvider(t, newTestKey(t)).URL
	other := newFakeProvider(t, newTestKey(t)).URL

	tests := []struct {
		name    string
		readers []string
	}{
		{"main issuer", []string{main}},
		{"twice", []string{other, other}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewOIDCVerifier(t.Context(), OIDCConfig{IssuerURL: main, Audience: testAudience, ReadOnlyIssuerURLs: tt.readers})
			if err == nil {
				t.Fatal("want an error for a duplicate issuer")
			}
		})
	}
}

func TestProxyAuth(t *testing.T) {
	t.Parallel()

	const header, dn = "X-SSL-Client-DN", "CN=alice,O=Example"

	handler, iss := readOnlyTestHandler(t, ProxyAuthConfig{
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
		{"bearer token wins", http.MethodPost, pathStreets, trustedPeer, dn, iss.sign(iss.main, iss.mainKey, testAudience), http.StatusOK, testSubject},
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
