package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/nils-witt/tmd-address/internal/buildinfo"
)

// registerVersionRoutes serves the build information at GET /api/version
// outside the auth middleware, as on the api: it tells
// deployments and monitoring which build runs without needing a token.
func registerVersionRoutes(rg gin.IRouter, info buildinfo.Info) {
	get(rg, "/api/version", func(c *gin.Context) {
		writeJSON(c, http.StatusOK, info)
	})
}
