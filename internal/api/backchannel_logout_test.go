package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nils-witt/tmd-address/internal/logout"
)

const (
	testClientID = "address-ui"
	testSID      = "sid-1"
	sidClaim     = "sid"
)

type fakeLogoutReceiver struct {
	called bool
	logout logout.Logout
	err    error
}

func (f *fakeLogoutReceiver) BackchannelLogout(_ context.Context, l logout.Logout) error {
	f.called = true
	f.logout = l

	return f.err
}

type fakeRevocations struct{ subject, sid string }

func (f fakeRevocations) Revoked(subject, sid string, _ time.Time) bool {
	return subject == f.subject && (f.sid == "" || sid == f.sid)
}

type logoutFixture struct {
	key      *rsa.PrivateKey
	issuer   string
	verifier *OIDCVerifier
}

func newLogoutFixture(t *testing.T) logoutFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	provider := newFakeProvider(t, key)

	v, err := NewOIDCVerifier(t.Context(), OIDCConfig{IssuerURL: provider.URL, Audience: testAudience, LogoutAudience: testClientID})
	if err != nil {
		t.Fatal(err)
	}

	return logoutFixture{key: key, issuer: provider.URL, verifier: v}
}

func (f logoutFixture) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKID

	s, err := token.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func (f logoutFixture) logoutClaims() jwt.MapClaims {
	now := time.Now()

	return jwt.MapClaims{
		issClaim: f.issuer,
		audClaim: testClientID,
		subClaim: testSubject,
		sidClaim: testSID,
		"iat":    now.Unix(),
		expClaim: now.Add(2 * time.Minute).Unix(),
		"jti":    "jti-1",
		"events": map[string]any{backchannelLogoutEvent: map[string]any{}},
	}
}

func postLogout(t *testing.T, handler http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()

	form := url.Values{}
	if token != "" {
		form.Set("logout_token", token)
	}

	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/auth/backchannel-logout", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	return w
}

func TestBackchannelLogout(t *testing.T) {
	t.Parallel()

	f := newLogoutFixture(t)
	with := func(mutate func(jwt.MapClaims)) string {
		c := f.logoutClaims()
		mutate(c)

		return f.sign(t, c)
	}

	tests := []struct {
		name        string
		token       string
		wantStatus  int
		wantSubject string
		wantSID     string
	}{
		{name: "sub and sid", token: f.sign(t, f.logoutClaims()), wantStatus: http.StatusOK, wantSubject: testSubject, wantSID: testSID},
		{name: "sid only", token: with(func(c jwt.MapClaims) { delete(c, subClaim) }), wantStatus: http.StatusOK, wantSID: testSID},
		{name: "sub only", token: with(func(c jwt.MapClaims) { delete(c, sidClaim) }), wantStatus: http.StatusOK, wantSubject: testSubject},
		{name: "missing token", wantStatus: http.StatusBadRequest},
		{name: "not a jwt", token: "garbage", wantStatus: http.StatusBadRequest},
		{name: "access token audience", token: with(func(c jwt.MapClaims) { c[audClaim] = testAudience }), wantStatus: http.StatusBadRequest},
		{name: "other issuer", token: with(func(c jwt.MapClaims) { c[issClaim] = "https://evil.example.com" }), wantStatus: http.StatusBadRequest},
		{name: "expired", token: with(func(c jwt.MapClaims) { c[expClaim] = time.Now().Add(-time.Hour).Unix() }), wantStatus: http.StatusBadRequest},
		{name: "missing iat", token: with(func(c jwt.MapClaims) { delete(c, "iat") }), wantStatus: http.StatusBadRequest},
		{name: "missing jti", token: with(func(c jwt.MapClaims) { delete(c, "jti") }), wantStatus: http.StatusBadRequest},
		{name: "neither sub nor sid", token: with(func(c jwt.MapClaims) { delete(c, subClaim); delete(c, sidClaim) }), wantStatus: http.StatusBadRequest},
		{name: "missing events", token: with(func(c jwt.MapClaims) { delete(c, "events") }), wantStatus: http.StatusBadRequest},
		{name: "event is no object", token: with(func(c jwt.MapClaims) { c["events"] = map[string]any{backchannelLogoutEvent: true} }), wantStatus: http.StatusBadRequest},
		{name: "empty nonce", token: with(func(c jwt.MapClaims) { c["nonce"] = "" }), wantStatus: http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			receiver := &fakeLogoutReceiver{}
			w := postLogout(t, handleBackchannelLogout(f.verifier, receiver), tc.token)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tc.wantStatus, w.Body.String())
			}

			if w.Header().Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
			}

			if tc.wantStatus != http.StatusOK {
				if receiver.called {
					t.Fatal("a rejected logout token was applied")
				}

				assertLogoutError(t, w, "invalid_request")

				return
			}

			got := receiver.logout
			if got.Subject != tc.wantSubject || got.SID != tc.wantSID || got.ID != logout.ID(f.issuer, "jti-1") || got.IssuedAt.IsZero() {
				t.Fatalf("logout = %+v, want subject %q and sid %q", got, tc.wantSubject, tc.wantSID)
			}
		})
	}
}

func TestBackchannelLogoutFailure(t *testing.T) {
	t.Parallel()

	f := newLogoutFixture(t)
	w := postLogout(t, handleBackchannelLogout(f.verifier, &fakeLogoutReceiver{err: errors.New("db down")}), f.sign(t, f.logoutClaims()))

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	assertLogoutError(t, w, "server_error")
}

func assertLogoutError(t *testing.T, w *httptest.ResponseRecorder, want string) {
	t.Helper()

	var body struct {
		Error string `json:"error"`
	}

	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error != want {
		t.Fatalf("body %q, want error %q", w.Body.String(), want)
	}
}

func TestAuthMiddlewareRefusesRevokedTokens(t *testing.T) {
	t.Parallel()

	f := newLogoutFixture(t)
	handler := authMiddleware(f.verifier, nil, fakeRevocations{subject: testSubject, sid: testSID})(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	access := func(sid string) string {
		return f.sign(t, jwt.MapClaims{
			issClaim: f.issuer, audClaim: testAudience, subClaim: testSubject, sidClaim: sid,
			"iat": time.Now().Unix(), expClaim: time.Now().Add(time.Hour).Unix(),
		})
	}

	tests := []struct {
		name string
		sid  string
		want int
	}{
		{name: "revoked session", sid: testSID, want: http.StatusUnauthorized},
		{name: "other session", sid: "sid-2", want: http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/streets", nil)
			r.Header.Set("Authorization", "Bearer "+access(tc.sid))

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}
