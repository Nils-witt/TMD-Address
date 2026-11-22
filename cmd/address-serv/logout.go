package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/nils-witt/address-serve/internal/logout"
	"github.com/nils-witt/address-serve/internal/logoutbus"
	"github.com/nils-witt/address-serve/internal/store"
)

// logoutConfig is how back-channel logouts are kept and shared with the
// other services (the API, the tile server) over RabbitMQ.
type logoutConfig struct {
	// amqpURL empty keeps the logouts to this service.
	amqpURL   string
	exchange  string
	queue     string
	retention time.Duration
}

// readLogoutConfig reads AMQP_URL, AMQP_LOGOUT_EXCHANGE, AMQP_LOGOUT_QUEUE
// and OIDC_LOGOUT_RETENTION.
func readLogoutConfig() (logoutConfig, error) {
	cfg := logoutConfig{
		amqpURL:  strings.TrimSpace(os.Getenv("AMQP_URL")),
		exchange: strings.TrimSpace(getenvDefault("AMQP_LOGOUT_EXCHANGE", defaultLogoutExchange)),
		queue:    strings.TrimSpace(getenvDefault("AMQP_LOGOUT_QUEUE", defaultLogoutQueue)),
	}

	var err error
	if cfg.retention, err = time.ParseDuration(getenvDefault("OIDC_LOGOUT_RETENTION", defaultLogoutRetention)); err != nil || cfg.retention <= 0 {
		return logoutConfig{}, errors.New("OIDC_LOGOUT_RETENTION must be a positive duration such as 24h")
	}

	if cfg.amqpURL == "" {
		return cfg, nil
	}

	if !strings.HasPrefix(cfg.amqpURL, "amqp://") && !strings.HasPrefix(cfg.amqpURL, "amqps://") {
		return logoutConfig{}, errors.New("AMQP_URL must start with amqp:// or amqps://")
	}

	if cfg.exchange == "" || cfg.queue == "" {
		return logoutConfig{}, errors.New("AMQP_LOGOUT_EXCHANGE and AMQP_LOGOUT_QUEUE must not be empty")
	}

	return cfg, nil
}

// startLogouts loads the logouts still in effect and keeps them in sync for
// as long as ctx lives; with AMQP_URL it also shares them with the other
// services.
func startLogouts(ctx context.Context, db *gorm.DB) (*logout.Service, error) {
	cfg, err := readLogoutConfig()
	if err != nil {
		return nil, err
	}

	deps := logout.Deps{Store: store.NewProviderLogoutStore(db), Retention: cfg.retention}

	var bus *logoutbus.Bus

	if cfg.amqpURL != "" {
		host, _ := os.Hostname()
		bus = logoutbus.New(logoutbus.Options{URL: cfg.amqpURL, Exchange: cfg.exchange, Queue: cfg.queue, Name: "address-serv " + host})
		deps.Broadcast = bus
	} else {
		log.Print("AMQP_URL is unset; back-channel logouts are not shared with the other services")
	}

	logouts := logout.NewService(deps)

	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := logouts.Load(loadCtx); err != nil {
		return nil, fmt.Errorf("failed to load back-channel logouts: %w", err)
	}

	go logouts.Run(ctx)

	if bus != nil {
		go bus.Run(ctx, logouts.Receive)
	}

	return logouts, nil
}
