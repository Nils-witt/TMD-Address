package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// UIConfig is what the web UI needs to know before anyone has signed in:
// where to send the user for an OpenID Connect login and which build it is
// talking to. It is served unauthenticated at GET /ui-config.
type UIConfig struct {
	OIDC    UIOIDCConfig `json:"oidc"`
	Version string       `json:"version"`
	Commit  string       `json:"commit"`
	// Branch is the git branch the build was made from; empty when unknown.
	Branch string `json:"branch"`
}

// UIOIDCConfig describes the public OIDC client the browser signs in with
// (authorization code flow with PKCE). The access token it obtains is sent to
// this API, so the provider must put the API's audience (OIDC_AUDIENCE) in it.
type UIOIDCConfig struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
	Scope    string `json:"scope"`
}

// registerUIRoutes mounts the SPA under /ui/ and its public config endpoint
// outside the auth middleware: the UI has to load before the user can sign
// in. "/" and "/ui" redirect to the UI.
func registerUIRoutes(rg gin.IRouter, ui http.Handler, cfg UIConfig) {
	rg.Any("/ui/*filepath", gin.WrapH(http.StripPrefix("/ui", ui)))
	get(rg, "/ui-config", func(c *gin.Context) {
		writeJSON(c, http.StatusOK, cfg)
	})

	redirect := func(c *gin.Context) { c.Redirect(http.StatusFound, "/ui/") }
	get(rg, "/", redirect)
	get(rg, "/ui", redirect)
}
