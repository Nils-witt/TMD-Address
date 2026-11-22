// Package logout applies sign-outs at the OpenID provider: those announced to
// address-serv by back-channel logout tokens, and those the other services
// (the API, the tile server) received, which arrive over the message broker
// (see logoutbus). Each revokes the access tokens issued before it.
package logout

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// Origin names this server in the logouts it announces.
const Origin = "address"

// ErrInvalid is wrapped by the errors for a logout that is not accepted.
var ErrInvalid = errors.New("invalid logout")

// ErrBrokerUnavailable is returned by a Broadcaster that is not connected to
// its broker.
var ErrBrokerUnavailable = errors.New("message broker unavailable")

// Logout is a sign-out at the OpenID provider. It revokes the access tokens
// of the provider session SID, or of every session of Subject when
// SID is empty.
type Logout struct {
	// ID identifies the logout across services (see ID), so receiving it
	// twice changes nothing.
	ID string
	// Issuer issued the logout token. Subjects are matched across issuers:
	// the provider gives a user the same sub in every application.
	Issuer  string
	Subject string
	SID     string
	// IssuedAt is the logout token's iat, on the provider's clock.
	IssuedAt time.Time
	// Origin is the service that received the logout token.
	Origin string
}

// ID is the ID of the logout announced by the token jti of issuer: the same in
// every service that receives it.
func ID(issuer, jti string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + jti))

	return hex.EncodeToString(sum[:])
}

// RevokesToken reports whether l revokes an access token of subject in the
// provider session sid, issued at issuedAt (both on the provider's clock). A
// token without iat (zero issuedAt) counts as issued before. A token without
// sid is revoked by a logout of its subject, even one naming a single session:
// which session it belongs to is unknown.
func (l Logout) RevokesToken(subject, sid string, issuedAt time.Time) bool {
	if issuedAt.After(l.IssuedAt) {
		return false
	}

	if l.SID != "" && sid != "" {
		return l.SID == sid && (l.Subject == "" || l.Subject == subject)
	}

	return l.Subject != "" && l.Subject == subject
}
