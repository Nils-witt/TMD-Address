package api

import (
	"net/http"
	"slices"
	"strings"
)

// corsMiddleware lets the browsers of allowedOrigins call the API:
// "*" for any origin, or a comma-separated list of origins. A listed origin
// is echoed with Access-Control-Allow-Credentials, as a browser sends its
// client certificate cross-origin only with credentials, and is never
// allowed together with "*".
func corsMiddleware(allowedOrigins string) func(http.Handler) http.Handler {
	var origins []string

	for o := range strings.SplitSeq(allowedOrigins, ",") {
		if o = strings.TrimSuffix(strings.TrimSpace(o), "/"); o != "" {
			origins = append(origins, o)
		}
	}

	anyOrigin := slices.Contains(origins, "*")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Add("Vary", "Origin")

			switch origin := r.Header.Get("Origin"); {
			case anyOrigin:
				h.Set("Access-Control-Allow-Origin", "*")
			case origin != "" && slices.Contains(origins, origin):
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
