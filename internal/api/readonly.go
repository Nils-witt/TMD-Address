package api

import (
	"net/http"
	"net/netip"
	"path"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
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

	// Cleaned, so "/api/streets/../tokens" counts as "/api/tokens" even
	// though the router would not match it.
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
func requireReadAccess(c *gin.Context) {
	if p, ok := principalFrom(c.Request.Context()); ok && p.ReadOnly && !readOnlyAllowed(c.Request) {
		writeError(c, http.StatusForbidden, "this token may only look addresses up")
	}
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
func proxyAuthMiddleware(cfg ProxyAuthConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg.Header == "" {
			return
		}

		r := c.Request
		if !cfg.trusted(r.RemoteAddr) {
			r.Header.Del(cfg.Header)
			return
		}

		// A bearer token says more than the proxy: it may grant writes.
		_, wsToken := webSocketToken(r)

		name := strings.TrimSpace(r.Header.Get(cfg.Header))
		if name == "" || r.Header.Get("Authorization") != "" || wsToken {
			return
		}

		setPrincipal(c, principal{Subject: name, Name: name, ReadOnly: true})
	}
}
