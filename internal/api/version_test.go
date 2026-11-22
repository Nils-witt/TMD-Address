package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nils-witt/tmd-address/internal/buildinfo"
)

// TestVersionBypassesAuth checks that GET /api/version answers the build
// information without a token.
func TestVersionBypassesAuth(t *testing.T) {
	t.Parallel()

	build := buildinfo.Info{
		Version: "v1.2.3", Commit: "39e8906", CommitTime: "2026-11-01T10:00:00Z",
		BuildTime: "2026-11-02T08:30:00Z", GoVersion: "go1.27.3",
	}
	// A nil verifier is never reached: the route bypasses auth.
	handler := NewHandler(Config{Build: build, CORSAllowedOrigin: "*"}, nil, nil, nil, nil, nil)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/version", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got buildinfo.Info
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got != build {
		t.Errorf("version = %+v, want %+v", got, build)
	}
}
