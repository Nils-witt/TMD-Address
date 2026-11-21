package main

import (
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// Defaults of the settings read in main.go, shared with logConfig so the
// startup log shows what is actually in effect.
const (
	defaultListenAddr        = ":8080"
	defaultCORSAllowedOrigin = "*"
	defaultPostgresHost      = "localhost"
	defaultPostgresPort      = "5432"
	defaultPostgresUser      = "address_serv"
	defaultPostgresPassword  = "address_serv"
	defaultPostgresDB        = "address_serv"
	defaultPostgresSSLMode   = "disable"
	defaultOIDCScope         = "openid profile email"
	defaultCollectorTimeout  = "10m"
	defaultCollectorGeoMaxM  = "100"
	defaultCollectorRuns     = "50"
)

// secretVisibleChars is how many trailing characters of a secret the startup
// log shows: enough to tell which secret is configured, not enough to use it.
const secretVisibleChars = 5

// configEntry is one environment variable as the startup log shows it.
type configEntry struct {
	key string
	// def describes the value used when key is unset.
	def string
	// mask hides the value, or the secret inside it, before logging.
	mask func(string) string
}

// configEntries lists every environment variable the server reads.
func configEntries() []configEntry {
	return []configEntry{
		{key: "LISTEN_ADDR", def: defaultListenAddr},
		{key: "DATABASE_URL", def: "built from POSTGRES_*", mask: maskDSN},
		{key: "POSTGRES_HOST", def: defaultPostgresHost},
		{key: "POSTGRES_PORT", def: defaultPostgresPort},
		{key: "POSTGRES_USER", def: defaultPostgresUser},
		{key: "POSTGRES_PASSWORD", def: maskSecret(defaultPostgresPassword), mask: maskSecret},
		{key: "POSTGRES_DB", def: defaultPostgresDB},
		{key: "POSTGRES_SSLMODE", def: defaultPostgresSSLMode},
		{key: "OIDC_ISSUER_URL"},
		{key: "OIDC_AUDIENCE"},
		{key: "OIDC_READONLY_AUDIENCES"},
		{key: "OIDC_JWKS_URL", def: "from OpenID discovery"},
		{key: "OIDC_CLIENT_ID", def: "OIDC_AUDIENCE"},
		{key: "OIDC_SCOPE", def: defaultOIDCScope},
		{key: "CORS_ALLOWED_ORIGIN", def: defaultCORSAllowedOrigin},
		{key: "PROXY_AUTH_HEADER"},
		{key: "TRUSTED_PROXIES"},
		{key: "COLLECTORS_DIR", def: "collectors folder next to the binary"},
		{key: "COLLECTOR_ADMIN_GROUP"},
		{key: "COLLECTOR_TIMEOUT", def: defaultCollectorTimeout},
		{key: "COLLECTOR_GEO_MAX_DISTANCE_M", def: defaultCollectorGeoMaxM},
		{key: "COLLECTOR_RUN_HISTORY", def: defaultCollectorRuns},
		{key: "TZ", def: "system time zone"},
	}
}

// logConfig logs each setting once at startup, so a misconfigured deployment
// shows what it runs with. Secrets are masked.
func logConfig() {
	for _, e := range configEntries() {
		//nolint:gosec // G706: formatConfigEntry quotes the value, so it can't inject lines.
		log.Print("config: " + formatConfigEntry(e, os.Getenv(e.key)))
	}
}

// formatConfigEntry renders one setting given its raw value.
func formatConfigEntry(e configEntry, value string) string {
	if value == "" {
		if e.def == "" {
			return e.key + " unset"
		}

		return e.key + " unset (default: " + e.def + ")"
	}

	if e.mask != nil {
		value = e.mask(value)
	}

	// Quoted, so a value holding a newline can't forge further log lines.
	return e.key + "=" + strconv.Quote(value)
}

// maskSecret keeps the last secretVisibleChars characters of s. A secret
// that short or shorter would be shown whole, so it is hidden entirely.
func maskSecret(s string) string {
	r := []rune(s)
	if len(r) <= 2*secretVisibleChars {
		return strings.Repeat("*", secretVisibleChars)
	}

	return strings.Repeat("*", secretVisibleChars) + string(r[len(r)-secretVisibleChars:])
}

// maskDSN masks the password in a postgres:// URL and keeps the rest
// readable. Anything it can't parse as such a URL, such as a key=value DSN,
// is masked as a whole, as it may hold a password anywhere.
func maskDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return maskSecret(dsn)
	}

	if pw, ok := u.User.Password(); ok {
		u.User = url.UserPassword(u.User.Username(), maskSecret(pw))
	}

	if q := u.Query(); q.Has("password") {
		q.Set("password", maskSecret(q.Get("password")))
		u.RawQuery = q.Encode()
	}

	return u.String()
}
