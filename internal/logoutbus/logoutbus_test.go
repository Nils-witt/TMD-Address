package logoutbus_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nils-witt/address-serve/internal/logout"
	"github.com/nils-witt/address-serve/internal/logoutbus"
)

const originAPI = "api"

func TestEncodeDecode(t *testing.T) {
	t.Parallel()

	l := logout.Logout{
		ID: "id", Issuer: "https://idp/", Subject: "alice", SID: "s1",
		IssuedAt: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC), Origin: originAPI,
	}

	body, err := logoutbus.Encode(l)
	if err != nil {
		t.Fatal(err)
	}

	got, err := logoutbus.Decode(body)
	if err != nil || got != l {
		t.Fatalf("Decode = %+v, %v; want %+v", got, err, l)
	}

	tests := []struct {
		name string
		body string
	}{
		{"not JSON", `nope`},
		{"unknown version", `{"schema_version":2,"id":"x"}`},
		{"no version", `{"id":"x"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := logoutbus.Decode([]byte(tt.body)); err == nil {
				t.Error("decoded")
			}
		})
	}
}

func TestPublishWithoutConnection(t *testing.T) {
	t.Parallel()

	b := logoutbus.New(logoutbus.Options{Exchange: "x", Queue: "q"})
	if err := b.Publish(t.Context(), logout.Logout{ID: "x"}); !errors.Is(err, logout.ErrBrokerUnavailable) {
		t.Errorf("err = %v, want logout.ErrBrokerUnavailable", err)
	}

	if b.Connected() {
		t.Error("connected without Run")
	}
}

// brokerURL returns TEST_AMQP_URL, a throwaway RabbitMQ the test declares
// exchanges and queues on, and skips the test without it.
func brokerURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("TEST_AMQP_URL")
	if url == "" {
		t.Skip("TEST_AMQP_URL is not set")
	}

	return url
}

// TestBusFansOutToEveryServiceQueue runs two services' buses on one
// exchange: a logout one publishes reaches both, and a malformed message is
// dropped instead of blocking the queue.
func TestBusFansOutToEveryServiceQueue(t *testing.T) {
	t.Parallel()
	url := brokerURL(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// A fresh exchange, so earlier runs' queues and messages don't interfere.
	exchange := fmt.Sprintf("test.oidc.logout.%d", time.Now().UnixNano())

	received := map[string]chan logout.Logout{originAPI: make(chan logout.Logout, 4), "tiles": make(chan logout.Logout, 4)}
	buses := map[string]*logoutbus.Bus{}

	for service, ch := range received {
		b := logoutbus.New(logoutbus.Options{URL: url, Exchange: exchange, Queue: exchange + "." + service, Name: service})

		buses[service] = b
		go b.Run(ctx, func(_ context.Context, l logout.Logout) error {
			if l.ID == "bad" {
				return fmt.Errorf("%w: bad", logout.ErrInvalid)
			}

			ch <- l

			return nil
		})
	}

	waitConnected(t, buses[originAPI], buses["tiles"])

	bad := logout.Logout{ID: "bad", Subject: "x", IssuedAt: time.Now(), Origin: originAPI}
	if err := buses[originAPI].Publish(ctx, bad); err != nil {
		t.Fatal(err)
	}

	l := logout.Logout{ID: "l1", Issuer: "https://idp/", Subject: "alice", IssuedAt: time.Now().UTC().Truncate(time.Second), Origin: originAPI}
	if err := buses[originAPI].Publish(ctx, l); err != nil {
		t.Fatal(err)
	}

	for service, ch := range received {
		select {
		case got := <-ch:
			if got != l {
				t.Errorf("%s received %+v, want %+v", service, got, l)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s received nothing", service)
		}
	}
}

func waitConnected(t *testing.T, buses ...*logoutbus.Bus) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)

	for _, b := range buses {
		for !b.Connected() {
			if time.Now().After(deadline) {
				t.Fatal("bus did not connect")
			}

			time.Sleep(50 * time.Millisecond)
		}
	}
}
