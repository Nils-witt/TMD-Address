package api

import (
	"net/http"

	"github.com/nils-witt/address-serve/internal/buildinfo"
)

// registerVersionRoutes serves the build information at GET /api/version on
// mux, which is served without the auth middleware, as on the api: it tells
// deployments and monitoring which build runs without needing a token.
func registerVersionRoutes(mux *http.ServeMux, info buildinfo.Info) {
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, info)
	})
}
