// Package api implements the address-serv HTTP API.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/klauspost/compress/gzhttp"

	"github.com/nils-witt/address-serve/internal/buildinfo"
	"github.com/nils-witt/address-serve/internal/store"
)

// Config holds the settings NewHandler needs besides the stores.
type Config struct {
	// OIDC verifies bearer tokens issued by an OpenID Connect provider. API
	// tokens are checked against the APITokenStore passed to NewHandler.
	OIDC *OIDCVerifier
	// ProxyAuth authenticates the clients a trusted reverse proxy names,
	// read-only.
	ProxyAuth ProxyAuthConfig
	// CORSAllowedOrigin is "*" or a comma-separated list of the origins
	// allowed to call the API from a browser.
	CORSAllowedOrigin string
	// Suggest serves /api/suggest/*. Nil leaves those routes out.
	Suggest *store.SuggestStore
	// UI serves the embedded web UI under /ui/. Nil disables the UI routes.
	UI http.Handler
	// UIConfig is served to the web UI at GET /ui-config.
	UIConfig UIConfig
	// Collectors wires the collector routes, which need the admin group.
	Collectors CollectorConfig
	// Build is served at GET /api/version.
	Build buildinfo.Info
	// Logouts applies the provider's back-channel logout tokens at
	// POST /auth/backchannel-logout; nil leaves the route out.
	Logouts LogoutReceiver
	// Revocations refuses the OIDC tokens a back-channel logout revoked;
	// nil refuses none.
	Revocations TokenRevocations
}

// NewHandler returns the root handler serving every API route, wrapped in
// logging, compression, CORS and auth middleware. The web UI, its config, the
// API docs, the build information and the back-channel logout (authenticated
// by its logout token) are the only routes served without auth.
func NewHandler(
	cfg Config,
	streets *store.StreetStore,
	houseNumbers *store.HouseNumberStore,
	geoObjects *store.GeoObjectStore,
	bulk *store.BulkStore,
	tokens *store.APITokenStore,
) http.Handler {
	mux := http.NewServeMux()
	registerStreetRoutes(mux, streets)
	registerHouseNumberRoutes(mux, houseNumbers)
	registerGeoObjectRoutes(mux, geoObjects)
	registerReverseRoutes(mux, houseNumbers, geoObjects)
	registerAddressSearchRoutes(mux, houseNumbers)
	registerBulkRoutes(mux, bulk)
	registerAPITokenRoutes(mux, tokens)

	if cfg.Suggest != nil {
		registerSuggestRoutes(mux, cfg.Suggest, houseNumbers, geoObjects)
	}

	if cfg.Collectors.Runner != nil {
		cfg.Collectors.Revocations = cfg.Revocations
		registerCollectorRoutes(mux, cfg.Collectors)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	root := http.NewServeMux()
	registerDocsRoutes(root)
	registerVersionRoutes(root, cfg.Build)

	if cfg.UI != nil {
		registerUIRoutes(root, cfg.UI, cfg.UIConfig)
	}

	if cfg.Logouts != nil {
		root.HandleFunc("POST /auth/backchannel-logout", handleBackchannelLogout(cfg.OIDC, cfg.Logouts))
	}

	// A nil *APITokenStore must become a nil interface, not a non-nil one
	// wrapping a nil pointer.
	var tokenAuth apiTokenAuthenticator
	if tokens != nil {
		tokenAuth = tokens
	}

	root.Handle("/", proxyAuthMiddleware(cfg.ProxyAuth)(authMiddleware(cfg.OIDC, tokenAuth, cfg.Revocations)(requireReadAccess(mux))))

	// gzip shrinks the unfiltered street list ~5x (833 KB → 156 KB for 5,000
	// streets) and the UI bundles ~3x; responses under 1 KB, already
	// compressed content types and range requests are passed through as is.
	// WebSocket handshakes skip it: their connection is hijacked, and
	// messages are not HTTP bodies it could compress.
	inner := corsMiddleware(cfg.CORSAllowedOrigin)(root)
	gzipped := gzhttp.GzipHandler(inner)

	return loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			inner.ServeHTTP(w, r)
			return
		}

		gzipped.ServeHTTP(w, r)
	}))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func idFromRequest(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(r.PathValue("id"))
}
