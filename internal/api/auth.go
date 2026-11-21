package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/nils-witt/address-serve/internal/store"
)

type contextKey string

const principalContextKey contextKey = "principal"

var (
	errMissingAuthHeader   = errors.New("missing Authorization header")
	errMalformedAuthHeader = errors.New("authorization header must be in the form 'Bearer <token>'")
)

// principal is who a request was authenticated as.
type principal struct {
	// Subject is the OIDC "sub" of the user, or of the API token's owner.
	Subject string
	// Name is a display name for the user, for recording who owns a token.
	Name string
	// APITokenID is set when the request carried an API token rather than an
	// OIDC token.
	APITokenID *uuid.UUID
	// Claims are the verified claims of an OIDC token; nil for API tokens.
	Claims jwt.MapClaims
	// ReadOnly principals may only look addresses up (see readOnlyRoutes):
	// OIDC tokens from a read-only issuer, and clients a trusted proxy
	// authenticated.
	ReadOnly bool
}

// principalFrom returns the principal authMiddleware stored in ctx.
func principalFrom(ctx context.Context) (principal, bool) {
	p, ok := ctx.Value(principalContextKey).(principal)
	return p, ok
}

// apiTokenAuthenticator resolves an API token secret to its stored token.
type apiTokenAuthenticator interface {
	Authenticate(ctx context.Context, secret string) (store.APIToken, error)
}

// authMiddleware requires a bearer token on every request: either a JWT
// issued by the configured OpenID Connect provider, or an API token (told
// apart by store.APITokenPrefix).
func authMiddleware(verifier *OIDCVerifier, tokens apiTokenAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Authenticated by a trusted proxy already.
			if _, ok := principalFrom(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}

			tokenString, err := bearerToken(r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}

			var p principal
			if strings.HasPrefix(tokenString, store.APITokenPrefix) {
				p, err = apiTokenPrincipal(r.Context(), tokens, tokenString)
			} else {
				p, err = oidcPrincipal(verifier, tokenString)
				// The response does not say why a token was rejected, so the
				// log has to, or a misconfigured issuer or audience looks
				// like any bad token. Expiry is routine and left out.
				if err != nil && !errors.Is(err, jwt.ErrTokenExpired) {
					log.Printf("OIDC token rejected: %v", err)
				}
			}

			if err != nil {
				writeError(w, http.StatusUnauthorized, "invalid or expired token")
				return
			}

			ctx := context.WithValue(r.Context(), principalContextKey, p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func oidcPrincipal(verifier *OIDCVerifier, tokenString string) (principal, error) {
	claims, readOnly, err := verifier.verify(tokenString)
	if err != nil {
		return principal{}, err
	}

	// A token without "sub" still authenticates; only owning API tokens
	// needs a subject (see requireTokenOwner).
	sub, _ := claims.GetSubject()

	return principal{Subject: sub, Name: displayName(claims, sub), Claims: claims, ReadOnly: readOnly}, nil
}

func apiTokenPrincipal(ctx context.Context, tokens apiTokenAuthenticator, secret string) (principal, error) {
	if tokens == nil {
		return principal{}, store.ErrInvalidAPIToken
	}

	t, err := tokens.Authenticate(ctx, secret)
	if err != nil {
		if !errors.Is(err, store.ErrInvalidAPIToken) {
			log.Printf("API token lookup failed: %v", err)
		}

		return principal{}, err
	}

	return principal{Subject: t.OwnerSub, Name: t.OwnerName, APITokenID: &t.ID}, nil
}

// displayName picks the most readable name claim present, falling back to sub.
func displayName(claims jwt.MapClaims, sub string) string {
	for _, key := range []string{"preferred_username", "name", "email"} {
		if v, ok := claims[key].(string); ok && v != "" {
			return v
		}
	}

	return sub
}

func bearerToken(r *http.Request) (string, error) {
	header := r.Header.Get("Authorization")
	if header == "" {
		// Browsers can't set headers on a WebSocket handshake, so the UI
		// sends its token as a subprotocol instead.
		if token, ok := webSocketToken(r); ok {
			return token, nil
		}

		return "", errMissingAuthHeader
	}

	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", errMalformedAuthHeader
	}

	return token, nil
}
