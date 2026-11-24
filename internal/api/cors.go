package api

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

// corsMiddleware lets the browsers of allowedOrigins call the API:
// "*" for any origin, or a comma-separated list of origins. A listed origin
// is echoed with Access-Control-Allow-Credentials, as a browser sends its
// client certificate cross-origin only with credentials, and is never
// allowed together with "*".
func corsMiddleware(allowedOrigins string) gin.HandlerFunc {
	var origins []string

	for o := range strings.SplitSeq(allowedOrigins, ",") {
		if o = strings.TrimSuffix(strings.TrimSpace(o), "/"); o != "" {
			origins = append(origins, o)
		}
	}

	anyOrigin := slices.Contains(origins, "*")

	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Add("Vary", "Origin")

		switch origin := c.GetHeader("Origin"); {
		case anyOrigin:
			h.Set("Access-Control-Allow-Origin", "*")
		case origin != "" && slices.Contains(origins, origin):
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
		}

		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// No route is registered for OPTIONS: preflights end here, on every
		// path, as Gin runs global middleware before NoRoute and NoMethod.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
		}
	}
}
