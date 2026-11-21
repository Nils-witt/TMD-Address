package api

import (
	"context"
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strings"
)

// readOnlyRoutes are the path prefixes a read-only principal may GET: the
// address lookups. Token, collector and bulk routes stay out of reach.
var readOnlyRoutes = []string{
	"/api/streets",
	"/api/house-numbers",
	"/api/cities",
	"/api/districts",
	"/api/geo-objects",
	"/api/reverse",
	"/api/addresses/search",
	"/api/suggest",
}

// readOnlyAllowed reports whether a read-only principal may make r.
func readOnlyAllowed(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}

	// Cleaned as ServeMux will, so "/api/streets/../tokens" counts as
	// "/api/tokens".
	p := path.Clean(r.URL.Path)
	if p == "/healthz" {
		return true
	}

	return slices.ContainsFunc(readOnlyRoutes, func(prefix string) bool {
		return p == prefix || strings.HasPrefix(p, prefix+"/")
	})
}

// requireReadAccess answers 403 to a read-only principal's requests other
// than looking addresses up.
func requireReadAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p, ok := principalFrom(r.Context()); ok && p.ReadOnly && !readOnlyAllowed(r) {
			writeError(w, http.StatusForbidden, "this token may only look addresses up")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ProxyAuthConfig configures authentication by a reverse proxy, such as one
// terminating mTLS: it verifies the client and names it in Header.
type ProxyAuthConfig struct {
	// Header names the client, e.g. X-SSL-Client-DN with the client
	// certificate's subject DN. Empty disables proxy authentication.
	Header string
	// TrustedProxies are the addresses Header is accepted from.
	TrustedProxies []netip.Prefix
}

// trusted reports whether remoteAddr, a request's RemoteAddr, is one of the
// trusted proxies.
func (c ProxyAuthConfig) trusted(remoteAddr string) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	if err != nil {
		return false
	}

	addr := ap.Addr().Unmap()

	return slices.ContainsFunc(c.TrustedProxies, func(p netip.Prefix) bool { return p.Contains(addr) })
}

// proxyAuthMiddleware authenticates a request without a bearer token as the
// client a trusted proxy names in cfg.Header, read-only. The header is
// dropped from other peers, so it cannot be forged around the proxy.
func proxyAuthMiddleware(cfg ProxyAuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if cfg.Header == "" {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.trusted(r.RemoteAddr) {
				r.Header.Del(cfg.Header)
				next.ServeHTTP(w, r)

				return
			}

			// A bearer token says more than the proxy: it may grant writes.
			_, wsToken := webSocketToken(r)

			name := strings.TrimSpace(r.Header.Get(cfg.Header))
			if name == "" || r.Header.Get("Authorization") != "" || wsToken {
				next.ServeHTTP(w, r)
				return
			}

			p := principal{Subject: name, Name: name, ReadOnly: true}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey, p)))
		})
	}
}
