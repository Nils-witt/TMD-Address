package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
)

// oidcSigningMethods are the asymmetric algorithms accepted for tokens issued
// by the OpenID provider. HMAC is deliberately excluded so a token can never be
// verified against a public key used as an HMAC secret.
var oidcSigningMethods = []string{
	"RS256", "RS384", "RS512",
	"PS256", "PS384", "PS512",
	"ES256", "ES384", "ES512",
	"EdDSA",
}

// OIDCConfig configures verification of JWTs issued by an OpenID Connect
// provider.
type OIDCConfig struct {
	// IssuerURL is the provider's issuer identifier. It must match the "iss"
	// claim of accepted tokens exactly.
	IssuerURL string
	// Audience must be contained in the "aud" claim of accepted tokens,
	// usually the client ID registered for this API.
	Audience string
	// ReadOnlyIssuerURLs are further issuers whose tokens are accepted for
	// reading addresses only (see readOnlyRoutes), whatever their audience:
	// those of other applications whose users may look addresses up here,
	// such as a browser app suggesting them. Providers such as Authentik
	// give every application its own issuer and signing key, so another
	// application's tokens are told apart by issuer, not by audience. Their
	// key sets are found via OpenID discovery.
	ReadOnlyIssuerURLs []string
	// JWKSURL overrides the key set URL of IssuerURL found via OpenID
	// discovery.
	JWKSURL string
}

// oidcIssuer verifies the tokens of one issuer against its own key set, so
// one issuer's key can never vouch for a token claiming another issuer.
type oidcIssuer struct {
	keys     keyfunc.Keyfunc
	parser   *jwt.Parser
	readOnly bool
}

// OIDCVerifier verifies access and ID tokens against the signing keys
// published by the OpenID Connect providers it accepts.
type OIDCVerifier struct {
	issuers map[string]oidcIssuer
}

// NewOIDCVerifier resolves each issuer's JWKS endpoint (via discovery unless
// cfg.JWKSURL is set for the main issuer) and keeps the key sets refreshed in
// the background until ctx is cancelled.
func NewOIDCVerifier(ctx context.Context, cfg OIDCConfig) (*OIDCVerifier, error) {
	if cfg.IssuerURL == "" {
		return nil, errors.New("OIDC issuer URL must be set")
	}

	if cfg.Audience == "" {
		return nil, errors.New("OIDC audience must be set")
	}

	main, err := newOIDCIssuer(ctx, cfg.IssuerURL, cfg.JWKSURL, jwt.WithAudience(cfg.Audience))
	if err != nil {
		return nil, err
	}

	v := &OIDCVerifier{issuers: map[string]oidcIssuer{cfg.IssuerURL: main}}

	for _, issuer := range cfg.ReadOnlyIssuerURLs {
		if issuer == "" {
			continue
		}

		if _, ok := v.issuers[issuer]; ok {
			// Silently demoting or ignoring it would leave the operator
			// guessing which access its tokens get.
			return nil, fmt.Errorf("read-only OIDC issuer %s is configured twice or is also the main issuer", issuer)
		}

		ro, err := newOIDCIssuer(ctx, issuer, "")
		if err != nil {
			return nil, fmt.Errorf("read-only OIDC issuer: %w", err)
		}

		ro.readOnly = true
		v.issuers[issuer] = ro
	}

	return v, nil
}

func newOIDCIssuer(ctx context.Context, issuer, jwksURL string, opts ...jwt.ParserOption) (oidcIssuer, error) {
	if jwksURL == "" {
		var err error

		jwksURL, err = discoverJWKSURL(ctx, issuer)
		if err != nil {
			return oidcIssuer{}, err
		}
	}

	keys, err := keyfunc.NewDefaultCtx(ctx, []string{jwksURL})
	if err != nil {
		return oidcIssuer{}, fmt.Errorf("failed to load OIDC key set from %s: %w", jwksURL, err)
	}

	return oidcIssuer{
		keys: keys,
		parser: jwt.NewParser(append([]jwt.ParserOption{
			jwt.WithValidMethods(oidcSigningMethods),
			jwt.WithIssuer(issuer),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(30 * time.Second),
		}, opts...)...),
	}, nil
}

// errUnknownIssuer is returned for a token from none of the accepted
// issuers.
var errUnknownIssuer = errors.New("token is from an unknown issuer")

// verify returns the claims of a valid token, and whether it was issued by a
// read-only issuer.
func (v *OIDCVerifier) verify(tokenString string) (jwt.MapClaims, bool, error) {
	// The issuer picks the key set to verify against, so it is read before
	// the signature is checked; the issuer's parser checks it again.
	unverified := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(tokenString, unverified); err != nil {
		return nil, false, err
	}

	iss, err := unverified.GetIssuer()
	if err != nil {
		return nil, false, err
	}

	issuer, ok := v.issuers[iss]
	if !ok {
		return nil, false, fmt.Errorf("%w: %q", errUnknownIssuer, iss)
	}

	claims := jwt.MapClaims{}

	token, err := issuer.parser.ParseWithClaims(tokenString, claims, issuer.keys.Keyfunc)
	if err != nil {
		return nil, false, err
	}

	if !token.Valid {
		return nil, false, errors.New("token is not valid")
	}

	return claims, issuer.readOnly, nil
}

// discoverJWKSURL fetches the provider's OpenID configuration document and
// returns its jwks_uri.
func discoverJWKSURL(ctx context.Context, issuer string) (string, error) {
	discoveryURL := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to build OIDC discovery request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch OIDC discovery document: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("OIDC discovery document at %s returned status %d", discoveryURL, resp.StatusCode)
	}

	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("failed to decode OIDC discovery document: %w", err)
	}

	if doc.Issuer != issuer {
		return "", fmt.Errorf("OIDC discovery issuer %q does not match configured issuer %q", doc.Issuer, issuer)
	}

	if doc.JWKSURI == "" {
		return "", errors.New("OIDC discovery document has no jwks_uri")
	}

	return doc.JWKSURI, nil
}
