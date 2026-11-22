package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nils-witt/tmd-address/internal/servicebus"
	"github.com/nils-witt/tmd-address/internal/store"
)

const envDiscoverySecret = "SERVICE_DISCOVERY_SECRET"

type fakeServiceTokens struct {
	client, instance string
	ttl              time.Duration
	err              error
}

func (f *fakeServiceTokens) ReplaceServiceToken(_ context.Context, client, instance string, ttl time.Duration) (store.APIToken, string, error) {
	if f.err != nil {
		return store.APIToken{}, "", f.err
	}

	f.client, f.instance, f.ttl = client, instance, ttl
	expires := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

	return store.APIToken{Prefix: "asv_abcdefgh", ExpiresAt: &expires}, "asv_secret", nil
}

func TestTokenIssuer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		client    string
		storeErr  error
		wantToken string
		wantErr   bool
	}{
		{name: "allowed", client: defaultServiceTokenClients, wantToken: "asv_secret"},
		{name: "not allowed", client: "api", wantErr: true},
		{name: "store fails", client: defaultServiceTokenClients, storeErr: errors.New("db down"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tokens := &fakeServiceTokens{err: tt.storeErr}
			issue := tokenIssuer(tokens, []string{defaultServiceTokenClients}, time.Hour)

			token, expires, err := issue(t.Context(), servicebus.TokenRequest{Client: tt.client, ClientInstance: "i1"})
			if (err != nil) != tt.wantErr || token != tt.wantToken {
				t.Fatalf("issue = %q, %v; want %q, err=%v", token, err, tt.wantToken, tt.wantErr)
			}

			if tt.wantErr {
				return
			}

			if tokens.client != defaultServiceTokenClients || tokens.instance != "i1" || tokens.ttl != time.Hour || expires.IsZero() {
				t.Errorf("store got %+v, expires %v", tokens, expires)
			}
		})
	}
}

func TestReadDiscoveryConfig(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"

	tests := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"off", map[string]string{}, true},
		{"on", map[string]string{envDiscoverySecret: secret, "SERVICE_URL": "http://address:8080"}, true},
		{"short secret", map[string]string{envDiscoverySecret: "short", "SERVICE_URL": "http://address:8080"}, false},
		{"no URL", map[string]string{envDiscoverySecret: secret}, false},
		{"bad TTL", map[string]string{"SERVICE_TOKEN_TTL": "5m"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range []string{envDiscoverySecret, "SERVICE_URL", "SERVICE_TOKEN_TTL", "SERVICE_TOKEN_CLIENTS", "SERVICE_ANNOUNCE_INTERVAL"} {
				t.Setenv(key, tt.env[key])
			}

			cfg, err := readDiscoveryConfig()
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok=%v", err, tt.ok)
			}

			if tt.ok && (len(cfg.clients) != 1 || cfg.clients[0] != defaultServiceTokenClients) {
				t.Errorf("clients = %v, want the default", cfg.clients)
			}
		})
	}
}
