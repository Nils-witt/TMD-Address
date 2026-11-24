package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRouting pins down how the Gin router matches, where it could differ
// from the http.ServeMux it replaced. The handlers get nil stores: every
// request here is answered before a store is reached.
func TestRouting(t *testing.T) {
	t.Parallel()

	routes := newEngine()
	registerStreetRoutes(routes, nil)
	registerGeoObjectRoutes(routes, nil)

	ui := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	root := NewHandler(Config{CORSAllowedOrigin: "*", UI: ui}, nil, nil, nil, nil, nil)

	tests := []struct {
		name         string
		handler      http.Handler
		method       string
		target       string
		body         string
		wantStatus   int
		wantLocation string
	}{
		// "invalid JSON body" proves the route matched; a miss is a 404.
		{name: "escaped slash stays in one segment", handler: routes, method: http.MethodPut, target: "/api/geo-objects/by-external-id/a%2Fb", body: "{", wantStatus: http.StatusBadRequest},
		{name: "unescaped slash is another path", handler: routes, method: http.MethodPut, target: "/api/geo-objects/by-external-id/a/b", body: "{", wantStatus: http.StatusNotFound},
		{name: "static segment beats a parameter", handler: routes, method: http.MethodPost, target: "/api/geo-objects/bulk-delete", body: "{", wantStatus: http.StatusBadRequest},
		{name: "HEAD is served like GET", handler: routes, method: http.MethodHead, target: "/api/streets/nope", wantStatus: http.StatusBadRequest},
		{name: "unknown method on a known path", handler: routes, method: http.MethodPatch, target: pathStreets, wantStatus: http.StatusMethodNotAllowed},
		{name: "trailing slash is not redirected", handler: routes, method: http.MethodGet, target: "/api/streets/", wantStatus: http.StatusNotFound},
		{name: "preflight needs no token", handler: root, method: http.MethodOptions, target: pathStreets, wantStatus: http.StatusNoContent},
		{name: "API needs a token", handler: root, method: http.MethodGet, target: pathStreets, wantStatus: http.StatusUnauthorized},
		{name: "root redirects to the UI", handler: root, method: http.MethodGet, target: "/", wantStatus: http.StatusFound, wantLocation: "/ui/"},
		{name: "UI without slash redirects", handler: root, method: http.MethodGet, target: "/ui", wantStatus: http.StatusFound, wantLocation: "/ui/"},
		{name: "UI is served without a token", handler: root, method: http.MethodGet, target: "/ui/streets/1", wantStatus: http.StatusTeapot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.target, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			tt.handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}

			if got := rec.Header().Get("Location"); got != tt.wantLocation {
				t.Errorf("Location = %q, want %q", got, tt.wantLocation)
			}
		})
	}
}
