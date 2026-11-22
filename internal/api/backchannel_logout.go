package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/nils-witt/tmd-address/internal/logout"
)

// backchannelLogoutEvent in the "events" claim tells a logout token apart
// from any other JWT the provider signs for this client.
const backchannelLogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// maxLogoutRequestBytes caps the body of a request that stays
// unauthenticated until its logout token is verified.
const maxLogoutRequestBytes = 64 << 10

// LogoutReceiver applies a verified back-channel logout (logout.Service).
type LogoutReceiver interface {
	BackchannelLogout(ctx context.Context, l logout.Logout) error
}

// TokenRevocations tells which access tokens a sign-out at the provider
// revoked (logout.Service).
type TokenRevocations interface {
	Revoked(subject, sid string, issuedAt time.Time) bool
}

// errSignedOut rejects an OIDC token a back-channel logout revoked.
var errSignedOut = errors.New("token revoked: the user signed out")

// VerifyLogoutToken validates a back-channel logout token of the main issuer
// as section 2.6 of OpenID Connect Back-Channel Logout 1.0 requires:
// signature, alg, iss, aud (the client ID), exp and iat, the logout event, a
// jti, sub or sid, and no nonce.
func (v *OIDCVerifier) VerifyLogoutToken(tokenString string) (logout.Logout, error) {
	claims := jwt.MapClaims{}

	token, err := v.logout.ParseWithClaims(tokenString, claims, v.keys.Keyfunc)
	if err != nil {
		return logout.Logout{}, fmt.Errorf("verify: %w", err)
	}

	if !token.Valid {
		return logout.Logout{}, errors.New("token is not valid")
	}

	return logoutFromClaims(claims)
}

func logoutFromClaims(claims jwt.MapClaims) (logout.Logout, error) {
	iat, err := claims.GetIssuedAt()
	if err != nil || iat == nil {
		return logout.Logout{}, errors.New("missing iat claim")
	}

	iss, _ := claims.GetIssuer()
	sub, _ := claims.GetSubject()
	jti, _ := claims["jti"].(string)
	sid, _ := claims["sid"].(string)
	events, _ := claims["events"].(map[string]any)
	_, isObject := events[backchannelLogoutEvent].(map[string]any)
	_, hasNonce := claims["nonce"]

	switch {
	case jti == "":
		return logout.Logout{}, errors.New("missing jti claim")
	case sub == "" && sid == "":
		return logout.Logout{}, errors.New("neither sub nor sid claim present")
	case !isObject:
		return logout.Logout{}, errors.New("events claim lacks the back-channel logout event")
	case hasNonce:
		return logout.Logout{}, errors.New("nonce claim present")
	}

	return logout.Logout{ID: logout.ID(iss, jti), Issuer: iss, Subject: sub, SID: sid, IssuedAt: iat.Time}, nil
}

// handleBackchannelLogout serves POST /auth/backchannel-logout (OpenID
// Connect Back-Channel Logout 1.0). The provider calls it when a user's
// session there ends; the logout token is the only authentication. As the
// spec requires, it answers 200 or 400 with an OAuth-style JSON error,
// neither cacheable.
func handleBackchannelLogout(verifier *OIDCVerifier, logouts LogoutReceiver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")

		r.Body = http.MaxBytesReader(w, r.Body, maxLogoutRequestBytes)

		rawToken := r.PostFormValue("logout_token")
		if rawToken == "" {
			writeLogoutError(w, "invalid_request", "missing logout_token")
			return
		}

		l, err := verifier.VerifyLogoutToken(rawToken)
		if err != nil {
			log.Printf("back-channel logout token rejected: %v", err)
			writeLogoutError(w, "invalid_request", "invalid logout_token")

			return
		}

		if err := logouts.BackchannelLogout(r.Context(), l); err != nil {
			log.Printf("back-channel logout failed: %v", err)
			writeLogoutError(w, "server_error", "the logout could not be applied")

			return
		}

		w.WriteHeader(http.StatusOK)
	}
}

func writeLogoutError(w http.ResponseWriter, code, description string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": description})
}

// tokenRevoked reports whether a back-channel logout revoked the OIDC token
// with claims.
func tokenRevoked(revoked TokenRevocations, claims jwt.MapClaims) bool {
	if revoked == nil || claims == nil {
		return false
	}

	sub, _ := claims.GetSubject()
	sid, _ := claims["sid"].(string)

	var issuedAt time.Time
	if iat, err := claims.GetIssuedAt(); err == nil && iat != nil {
		issuedAt = iat.Time
	}

	return revoked.Revoked(sub, sid, issuedAt)
}
