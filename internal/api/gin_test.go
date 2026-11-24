package api

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
)

// serveGin serves handlers, in order, on a Gin engine for every method and
// path, so a test can drive middleware and handlers as one http.Handler.
func serveGin(handlers ...gin.HandlerFunc) http.Handler {
	engine := newEngine()
	engine.HandleMethodNotAllowed = false

	// Gin presets 404 before NoRoute; a handler that writes without setting
	// a status must still answer 200, as it would on a registered route.
	presetOK := func(c *gin.Context) { c.Status(http.StatusOK) }
	engine.NoRoute(append([]gin.HandlerFunc{presetOK}, handlers...)...)

	return engine
}

// writeTestJSON answers with v as JSON, for the fake servers that run
// outside Gin.
func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
