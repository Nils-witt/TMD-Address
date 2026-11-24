// Package api implements the address-serv HTTP API.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/klauspost/compress/gzhttp"

	"github.com/nils-witt/tmd-address/internal/buildinfo"
	"github.com/nils-witt/tmd-address/internal/store"
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
	// Transfer serves GET /api/export and POST /api/import. Nil leaves those
	// routes out.
	Transfer *store.TransferStore
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
	engine := newEngine()
	engine.Use(loggingMiddleware, corsMiddleware(cfg.CORSAllowedOrigin))

	registerDocsRoutes(engine)
	registerVersionRoutes(engine, cfg.Build)

	if cfg.UI != nil {
		registerUIRoutes(engine, cfg.UI, cfg.UIConfig)
	}

	if cfg.Logouts != nil {
		engine.POST("/auth/backchannel-logout", handleBackchannelLogout(cfg.OIDC, cfg.Logouts))
	}

	// A nil *APITokenStore must become a nil interface, not a non-nil one
	// wrapping a nil pointer.
	var tokenAuth apiTokenAuthenticator
	if tokens != nil {
		tokenAuth = tokens
	}

	authed := engine.Group("",
		proxyAuthMiddleware(cfg.ProxyAuth),
		authMiddleware(cfg.OIDC, tokenAuth, cfg.Revocations),
		requireReadAccess,
	)

	registerStreetRoutes(authed, streets)
	registerHouseNumberRoutes(authed, houseNumbers)
	registerGeoObjectRoutes(authed, geoObjects)
	registerReverseRoutes(authed, houseNumbers, geoObjects)
	registerAddressSearchRoutes(authed, houseNumbers)
	registerBulkRoutes(authed, bulk)
	registerAPITokenRoutes(authed, tokens)

	if cfg.Suggest != nil {
		registerSuggestRoutes(authed, cfg.Suggest, houseNumbers, geoObjects)
	}

	if cfg.Transfer != nil {
		registerTransferRoutes(authed, cfg.Transfer)
	}

	if cfg.Collectors.Runner != nil {
		cfg.Collectors.Revocations = cfg.Revocations
		registerCollectorRoutes(authed, cfg.Collectors)
	}

	get(authed, "/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })

	// gzip shrinks the unfiltered street list ~5x (833 KB → 156 KB for 5,000
	// streets) and the UI bundles ~3x; responses under 1 KB, already
	// compressed content types and range requests are passed through as is.
	// WebSocket handshakes skip it: their connection is hijacked, and
	// messages are not HTTP bodies it could compress.
	gzipped := gzhttp.GzipHandler(engine)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isWebSocketUpgrade(r) {
			engine.ServeHTTP(w, r)
			return
		}

		gzipped.ServeHTTP(w, r)
	})
}

// newEngine returns a Gin engine that routes like the http.ServeMux it
// replaced: matching on escaped path segments, so an external ID holding an
// escaped "/" stays one segment, answering 405 for a known path, and never
// redirecting to a cleaned or slash-trimmed path.
func newEngine() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	engine := gin.New()
	engine.HandleMethodNotAllowed = true
	engine.RedirectTrailingSlash = false
	engine.RedirectFixedPath = false
	engine.UseEscapedPath = true
	engine.UnescapePathValues = true
	engine.Use(gin.Recovery())

	// Nothing asks Gin for the client IP; proxy auth reads RemoteAddr against
	// TRUSTED_PROXIES itself. Trusting none keeps c.ClientIP from ever
	// believing a forwarded header.
	_ = engine.SetTrustedProxies(nil)

	return engine
}

// get registers h for GET and HEAD, as a "GET" ServeMux pattern did.
func get(rg gin.IRoutes, path string, h ...gin.HandlerFunc) {
	rg.GET(path, h...)
	rg.HEAD(path, h...)
}

func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// writeError answers with a JSON error and stops the handler chain, so a
// middleware rejecting a request needs no separate c.Abort.
func writeError(c *gin.Context, status int, msg string) {
	c.AbortWithStatusJSON(status, map[string]string{"error": msg})
}

func idFromRequest(c *gin.Context) (uuid.UUID, error) {
	return uuid.Parse(c.Param("id"))
}
