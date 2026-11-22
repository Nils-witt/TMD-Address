// Command address-serv serves the address lookup and management HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/joho/godotenv"

	"github.com/nils-witt/tmd-address/frontend"
	"github.com/nils-witt/tmd-address/internal/api"
	"github.com/nils-witt/tmd-address/internal/api/spa"
	"github.com/nils-witt/tmd-address/internal/buildinfo"
	"github.com/nils-witt/tmd-address/internal/collector"
	"github.com/nils-witt/tmd-address/internal/store"
)

// Build info, set at link time with -X (see Dockerfile and .goreleaser.yml).
// Empty values fall back to the VCS data Go embeds (see buildinfo.Get).
var (
	version   string
	commit    string
	branch    string
	buildTime string
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		runHealthcheck()
		return
	}

	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	if err := godotenv.Load(); err != nil {
		log.Print("no .env file found, relying on environment variables")
	}

	build := buildinfo.Get(version, commit, buildTime)
	log.Printf("address-serv %s (%s)", build.Version, build.Commit)
	logConfig()

	ctx := context.Background()

	oidc, err := oidcVerifier(ctx)
	if err != nil {
		return err
	}

	db, err := store.Open(ctx, databaseURL())
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer func() { _ = store.Close(db) }()

	if err := store.Migrate(ctx, db); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	ui, err := spa.Handler(frontend.DistFS)
	if err != nil {
		return fmt.Errorf("failed to load web UI: %w", err)
	}

	collectors, err := setUpCollectors(ctx, store.NewCollectorStore(db))
	if err != nil {
		return err
	}

	proxyAuth, err := proxyAuthConfig()
	if err != nil {
		return err
	}

	logouts, err := startLogouts(ctx, db)
	if err != nil {
		return err
	}

	handler := api.NewHandler(
		api.Config{
			OIDC:              oidc,
			ProxyAuth:         proxyAuth,
			CORSAllowedOrigin: getenvDefault("CORS_ALLOWED_ORIGIN", defaultCORSAllowedOrigin),
			Suggest:           store.NewSuggestStore(db),
			UI:                ui,
			UIConfig:          uiConfig(build),
			Collectors:        collectors,
			Build:             build,
			Logouts:           logouts,
			Revocations:       logouts,
		},
		store.NewStreetStore(db),
		store.NewHouseNumberStore(db),
		store.NewGeoObjectStore(db),
		store.NewBulkStore(db),
		store.NewAPITokenStore(db),
	)

	addr := getenvDefault("LISTEN_ADDR", defaultListenAddr)
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("listening on %s", addr)

	return server.ListenAndServe()
}

// setUpCollectors configures the collector runner from the COLLECTOR*
// variables, fails the runs a previous process left running, scans the
// collectors folder once, and starts the scheduler. A folder that can't be scanned is logged, not
// fatal: it can be fixed and reloaded from the UI.
func setUpCollectors(ctx context.Context, cs *store.CollectorStore) (api.CollectorConfig, error) {
	if !collector.Supported {
		log.Print("collectors are not supported on this OS; their routes are disabled")
		return api.CollectorConfig{}, nil
	}

	cfg, err := collectorConfig()
	if err != nil {
		return api.CollectorConfig{}, err
	}

	if n, err := cs.FailInterruptedRuns(ctx); err != nil {
		return api.CollectorConfig{}, fmt.Errorf("failed to recover collector runs: %w", err)
	} else if n > 0 {
		log.Printf("marked %d collector runs interrupted by the last shutdown as failed", n)
	}

	runner := collector.NewRunner(cs, cfg)

	found, err := runner.Reload(ctx)
	if err != nil {
		log.Printf("collectors: %v", err)
	} else {
		log.Printf("collectors: %d known, folder %s", len(found), cfg.Dir)
	}

	// The scheduler lives as long as the process; runs it started are
	// recovered by FailInterruptedRuns on the next start.
	go runner.RunSchedules(ctx)

	adminGroup := os.Getenv("COLLECTOR_ADMIN_GROUP")
	if adminGroup == "" {
		log.Print("COLLECTOR_ADMIN_GROUP is unset; nobody can manage collectors")
	}

	return api.CollectorConfig{Runner: runner, Store: cs, AdminGroup: adminGroup}, nil
}

// collectorConfig reads the runner's settings from the environment.
func collectorConfig() (collector.Config, error) {
	cfg := collector.Config{Dir: os.Getenv("COLLECTORS_DIR")}

	if cfg.Dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return collector.Config{}, fmt.Errorf("locate the collectors folder: %w; set COLLECTORS_DIR", err)
		}

		cfg.Dir = filepath.Join(filepath.Dir(exe), "collectors")
	}

	var err error

	if cfg.Timeout, err = time.ParseDuration(getenvDefault("COLLECTOR_TIMEOUT", defaultCollectorTimeout)); err != nil || cfg.Timeout <= 0 {
		return collector.Config{}, errors.New("COLLECTOR_TIMEOUT must be a positive duration such as 10m")
	}

	cfg.GeoMaxDistanceMeters, err = strconv.ParseFloat(getenvDefault("COLLECTOR_GEO_MAX_DISTANCE_M", defaultCollectorGeoMaxM), 64)
	if err != nil || cfg.GeoMaxDistanceMeters < 0 {
		return collector.Config{}, errors.New("COLLECTOR_GEO_MAX_DISTANCE_M must be a non-negative number of metres")
	}

	if cfg.RunHistory, err = strconv.Atoi(getenvDefault("COLLECTOR_RUN_HISTORY", defaultCollectorRuns)); err != nil || cfg.RunHistory < 1 {
		return collector.Config{}, errors.New("COLLECTOR_RUN_HISTORY must be a positive integer")
	}

	return cfg, nil
}

// oidcVerifier returns a verifier for tokens issued by the OpenID Connect
// provider at OIDC_ISSUER_URL.
func oidcVerifier(ctx context.Context) (*api.OIDCVerifier, error) {
	issuer := os.Getenv("OIDC_ISSUER_URL")
	if issuer == "" {
		return nil, errors.New("OIDC_ISSUER_URL environment variable must be set")
	}

	verifier, err := api.NewOIDCVerifier(ctx, api.OIDCConfig{
		IssuerURL:          issuer,
		Audience:           os.Getenv("OIDC_AUDIENCE"),
		ReadOnlyIssuerURLs: splitList(os.Getenv("OIDC_READONLY_ISSUER_URLS")),
		JWKSURL:            os.Getenv("OIDC_JWKS_URL"),
		// Logout tokens are issued to the client, like the UI's tokens.
		LogoutAudience: getenvDefault("OIDC_LOGOUT_AUDIENCE", os.Getenv("OIDC_CLIENT_ID")),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set up OIDC: %w", err)
	}

	return verifier, nil
}

// proxyAuthConfig reads PROXY_AUTH_HEADER and TRUSTED_PROXIES. The header
// is only accepted from trusted proxies, so it needs them.
func proxyAuthConfig() (api.ProxyAuthConfig, error) {
	cfg := api.ProxyAuthConfig{Header: strings.TrimSpace(os.Getenv("PROXY_AUTH_HEADER"))}

	for _, v := range splitList(os.Getenv("TRUSTED_PROXIES")) {
		p, err := netip.ParsePrefix(v)
		if err != nil {
			a, aerr := netip.ParseAddr(v)
			if aerr != nil {
				return api.ProxyAuthConfig{}, fmt.Errorf("TRUSTED_PROXIES: %q is neither an IP address nor a CIDR", v)
			}

			p = netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen())
		}

		cfg.TrustedProxies = append(cfg.TrustedProxies, p.Masked())
	}

	if cfg.Header != "" && len(cfg.TrustedProxies) == 0 {
		return api.ProxyAuthConfig{}, errors.New("PROXY_AUTH_HEADER requires TRUSTED_PROXIES: the header is only accepted from trusted proxies")
	}

	if cfg.Header != "" {
		log.Print("proxy authentication enabled for TRUSTED_PROXIES, read-only")
	}

	return cfg, nil
}

// splitList splits a list separated by commas or spaces.
func splitList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// uiConfig describes the OIDC client the web UI signs in with. The client ID
// defaults to OIDC_AUDIENCE, which fits providers where a public client's own
// ID ends up in the access token's audience.
func uiConfig(build buildinfo.Info) api.UIConfig {
	return api.UIConfig{
		OIDC: api.UIOIDCConfig{
			Issuer:   os.Getenv("OIDC_ISSUER_URL"),
			ClientID: getenvDefault("OIDC_CLIENT_ID", os.Getenv("OIDC_AUDIENCE")),
			Scope:    getenvDefault("OIDC_SCOPE", defaultOIDCScope),
		},
		Version: build.Version,
		Commit:  build.Commit,
		Branch:  branch,
	}
}

// databaseURL returns DATABASE_URL, or builds a DSN from the POSTGRES_*
// variables when it is unset.
func databaseURL() string {
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		return dsn
	}

	host := getenvDefault("POSTGRES_HOST", defaultPostgresHost)
	port := getenvDefault("POSTGRES_PORT", defaultPostgresPort)
	user := getenvDefault("POSTGRES_USER", defaultPostgresUser)
	password := getenvDefault("POSTGRES_PASSWORD", defaultPostgresPassword)
	dbname := getenvDefault("POSTGRES_DB", defaultPostgresDB)
	sslmode := getenvDefault("POSTGRES_SSLMODE", defaultPostgresSSLMode)

	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s", user, password, host, port, dbname, sslmode)
}

func getenvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
