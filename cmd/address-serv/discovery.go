package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/nils-witt/tmd-address/internal/buildinfo"
	"github.com/nils-witt/tmd-address/internal/servicebus"
	"github.com/nils-witt/tmd-address/internal/store"
)

// discoveryServiceName is what address-serv announces itself as.
const discoveryServiceName = "address"

// errClientNotAllowed refuses a token request from a service not listed in
// SERVICE_TOKEN_CLIENTS.
var errClientNotAllowed = errors.New("this service may not get a token here")

// discoveryConfig is how address-serv announces itself to the other
// services over RabbitMQ and hands them API tokens.
type discoveryConfig struct {
	// secret empty turns service discovery off.
	secret   string
	url      string
	tokenTTL time.Duration
	clients  []string
	interval time.Duration
}

// readDiscoveryConfig reads SERVICE_DISCOVERY_SECRET, SERVICE_URL,
// SERVICE_TOKEN_TTL, SERVICE_TOKEN_CLIENTS and SERVICE_ANNOUNCE_INTERVAL.
func readDiscoveryConfig() (discoveryConfig, error) {
	cfg := discoveryConfig{
		secret: strings.TrimSpace(os.Getenv("SERVICE_DISCOVERY_SECRET")),
		url:    strings.TrimSpace(os.Getenv("SERVICE_URL")),
	}

	for client := range strings.SplitSeq(getenvDefault("SERVICE_TOKEN_CLIENTS", defaultServiceTokenClients), ",") {
		if client = strings.TrimSpace(client); client != "" {
			cfg.clients = append(cfg.clients, client)
		}
	}

	var err error
	if cfg.tokenTTL, err = time.ParseDuration(getenvDefault("SERVICE_TOKEN_TTL", defaultServiceTokenTTL)); err != nil || cfg.tokenTTL < time.Hour {
		return discoveryConfig{}, errors.New("SERVICE_TOKEN_TTL must be a duration of at least 1h, such as 168h")
	}

	if cfg.interval, err = time.ParseDuration(getenvDefault("SERVICE_ANNOUNCE_INTERVAL", defaultServiceAnnounce)); err != nil || cfg.interval < time.Second {
		return discoveryConfig{}, errors.New("SERVICE_ANNOUNCE_INTERVAL must be a duration of at least 1s, such as 1m")
	}

	if cfg.secret == "" {
		return cfg, nil
	}

	if len(cfg.secret) < servicebus.MinSecretLen {
		return discoveryConfig{}, fmt.Errorf("SERVICE_DISCOVERY_SECRET must be at least %d bytes", servicebus.MinSecretLen)
	}

	if u, err := url.Parse(cfg.url); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return discoveryConfig{}, errors.New("SERVICE_URL must be the http(s):// URL the other services reach address-serv at, e.g. http://address:8080")
	}

	return cfg, nil
}

// startDiscovery announces address-serv on the broker at amqpURL and
// answers the token requests of the allowed services, until ctx is done.
// It does nothing without SERVICE_DISCOVERY_SECRET or AMQP_URL.
func startDiscovery(ctx context.Context, db *gorm.DB, amqpURL string, build buildinfo.Info) error {
	cfg, err := readDiscoveryConfig()
	if err != nil {
		return err
	}

	if cfg.secret == "" {
		return nil
	}

	if amqpURL == "" {
		log.Print("SERVICE_DISCOVERY_SECRET is set but AMQP_URL is not; service discovery is off")
		return nil
	}

	host, _ := os.Hostname()

	bus, err := servicebus.New(servicebus.Options{
		URL:    amqpURL,
		Secret: []byte(cfg.secret),
		Name:   "address-serv discovery " + host,
		Self: servicebus.Announcement{
			Service: discoveryServiceName, InstanceID: host, Name: "address-serv " + host,
			URL: cfg.url, Version: build.Version,
		},
		AnnounceInterval: cfg.interval,
	})
	if err != nil {
		return fmt.Errorf("service discovery: %w", err)
	}

	bus.ServeTokens(tokenIssuer(store.NewAPITokenStore(db), cfg.clients, cfg.tokenTTL))

	go bus.Run(ctx)

	log.Printf("service discovery on: announcing %s, tokens for %s", cfg.url, strings.Join(cfg.clients, ", "))

	return nil
}

// serviceTokenStore issues service tokens; *store.APITokenStore implements it.
type serviceTokenStore interface {
	ReplaceServiceToken(ctx context.Context, client, instance string, ttl time.Duration) (store.APIToken, string, error)
}

// tokenIssuer issues a token valid for ttl to the services in clients.
func tokenIssuer(tokens serviceTokenStore, clients []string, ttl time.Duration) servicebus.IssueFunc {
	return func(ctx context.Context, req servicebus.TokenRequest) (string, time.Time, error) {
		if !slices.Contains(clients, req.Client) {
			return "", time.Time{}, fmt.Errorf("%w: %q", errClientNotAllowed, req.Client)
		}

		t, secret, err := tokens.ReplaceServiceToken(ctx, req.Client, req.ClientInstance, ttl)
		if err != nil {
			log.Printf("service discovery: issuing a token to %s failed: %v", req.Client, err)
			return "", time.Time{}, errors.New("issuing the token failed")
		}

		log.Printf("service discovery: issued token %s… to %q instance %q", t.Prefix, req.Client, req.ClientInstance)

		var expiresAt time.Time
		if t.ExpiresAt != nil {
			expiresAt = *t.ExpiresAt
		}

		return secret, expiresAt, nil
	}
}
