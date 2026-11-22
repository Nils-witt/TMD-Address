// Package logoutbus shares sign-outs at the OpenID provider between the
// services over RabbitMQ.
//
// Every service publishes the back-channel logouts it receives to one
// durable fanout exchange (default "oidc.logout") and consumes them from its
// own durable queue bound to it (e.g. "oidc.logout.address"). The instances of a
// service share its queue, so each logout is handled once per service, and
// the broker keeps the logouts announced while a service is down until it
// is back. The instances learn of a logout another one handled from their
// shared database.
//
// A message is JSON (see Message) with schema_version 1. Fields may be added;
// removing or changing one needs a new schema_version, and consumers reject
// versions they do not know.
package logoutbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/nils-witt/tmd-address/internal/logout"
)

// SchemaVersion is the version of Message this package writes and reads.
const SchemaVersion = 1

// MessageType is the AMQP type property of a logout message.
const MessageType = "oidc.backchannel_logout"

const (
	minReconnectDelay = time.Second
	maxReconnectDelay = 30 * time.Second
	// retryDelay holds back a logout whose handling failed before it is
	// delivered again, so a database outage does not spin.
	retryDelay = 2 * time.Second
	prefetch   = 16
)

// Message is a logout on the wire.
type Message struct {
	SchemaVersion int    `json:"schema_version"`
	ID            string `json:"id"`
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject,omitempty"`
	SID           string `json:"sid,omitempty"`
	// IssuedAt is the logout token's iat.
	IssuedAt time.Time `json:"issued_at"`
	// Origin is the service that received the logout token.
	Origin string `json:"origin"`
}

// Encode returns l as a Message.
func Encode(l logout.Logout) ([]byte, error) {
	b, err := json.Marshal(Message{
		SchemaVersion: SchemaVersion, ID: l.ID, Issuer: l.Issuer, Subject: l.Subject, SID: l.SID,
		IssuedAt: l.IssuedAt.UTC(), Origin: l.Origin,
	})
	if err != nil {
		return nil, fmt.Errorf("encoding logout: %w", err)
	}

	return b, nil
}

// Decode reads a Message. A version it does not know is an error.
func Decode(body []byte) (logout.Logout, error) {
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return logout.Logout{}, fmt.Errorf("decoding logout: %w", err)
	}

	if m.SchemaVersion != SchemaVersion {
		return logout.Logout{}, fmt.Errorf("unsupported logout schema_version %d", m.SchemaVersion)
	}

	return logout.Logout{
		ID: m.ID, Issuer: m.Issuer, Subject: m.Subject, SID: m.SID, IssuedAt: m.IssuedAt, Origin: m.Origin,
	}, nil
}

// Options configures a Bus.
type Options struct {
	// URL is amqp://user:pass@host:5672/vhost or amqps://… for TLS.
	URL      string
	Exchange string
	Queue    string
	// Name names the connection in the broker's management UI.
	Name string
}

// Handler applies a received logout. An error wrapping logout.ErrInvalid
// drops the message; any other error has it delivered again.
type Handler func(ctx context.Context, l logout.Logout) error

// Bus publishes logouts and hands the received ones to a Handler. It
// implements logout.Broadcaster.
type Bus struct {
	opts Options

	mu sync.Mutex
	// pub is the confirming publisher channel, nil while disconnected.
	pub *amqp.Channel
}

var _ logout.Broadcaster = (*Bus)(nil)

// New creates a Bus; Run connects it.
func New(opts Options) *Bus {
	return &Bus{opts: opts}
}

// Connected reports whether the bus is connected to the broker.
func (b *Bus) Connected() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.pub != nil
}

// Publish sends l and returns once the broker confirmed it. Without a
// connection it fails with logout.ErrBrokerUnavailable at once.
func (b *Bus) Publish(ctx context.Context, l logout.Logout) error {
	body, err := Encode(l)
	if err != nil {
		return err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.pub == nil {
		return logout.ErrBrokerUnavailable
	}

	confirm, err := b.pub.PublishWithDeferredConfirmWithContext(ctx, b.opts.Exchange, "", false, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: l.ID,
		Type: MessageType, Timestamp: time.Now().UTC(), AppId: l.Origin, Body: body,
	})
	if err != nil {
		return fmt.Errorf("publishing logout: %w", err)
	}

	ok, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("waiting for the broker to confirm a logout: %w", err)
	}

	if !ok {
		return errors.New("the broker refused a logout")
	}

	return nil
}

// Run connects to the broker, declares the exchange and queue, and hands
// every received logout to handle until ctx is done, reconnecting with
// backoff when the connection is lost. It is meant to run in its own
// goroutine.
func (b *Bus) Run(ctx context.Context, handle Handler) {
	delay := minReconnectDelay

	for ctx.Err() == nil {
		start := time.Now()

		err := b.session(ctx, handle)
		if ctx.Err() != nil {
			return
		}

		if time.Since(start) > maxReconnectDelay {
			delay = minReconnectDelay // it was up a while: retry soon
		}

		slog.WarnContext(ctx, "lost the message broker connection", "error", err, "retry_in", delay)

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		delay = min(delay*2, maxReconnectDelay)
	}
}

// session runs one connection until it fails or ctx is done.
func (b *Bus) session(ctx context.Context, handle Handler) error {
	cfg := amqp.Config{Properties: amqp.NewConnectionProperties()}
	cfg.Properties.SetClientConnectionName(b.opts.Name)

	conn, err := amqp.DialConfig(b.opts.URL, cfg)
	if err != nil {
		return fmt.Errorf("connecting: %w", err)
	}

	defer func() { _ = conn.Close() }()

	deliveries, err := b.setup(ctx, conn)
	if err != nil {
		return err
	}
	defer b.setPublisher(nil)

	slog.InfoContext(ctx, "connected to the message broker", "exchange", b.opts.Exchange, "queue", b.opts.Queue)

	closed := conn.NotifyClose(make(chan *amqp.Error, 1))

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-closed:
			return fmt.Errorf("connection closed: %w", err)
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("consumer cancelled")
			}

			if err := b.deliver(ctx, d, handle); err != nil {
				return err
			}
		}
	}
}

// setup declares the topology, opens the confirming publisher channel and
// starts consuming.
func (b *Bus) setup(ctx context.Context, conn *amqp.Connection) (<-chan amqp.Delivery, error) {
	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("opening channel: %w", err)
	}

	if err := ch.ExchangeDeclare(b.opts.Exchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return nil, fmt.Errorf("declaring exchange %q: %w", b.opts.Exchange, err)
	}

	if _, err := ch.QueueDeclare(b.opts.Queue, true, false, false, false, nil); err != nil {
		return nil, fmt.Errorf("declaring queue %q: %w", b.opts.Queue, err)
	}

	if err := ch.QueueBind(b.opts.Queue, "", b.opts.Exchange, false, nil); err != nil {
		return nil, fmt.Errorf("binding queue %q: %w", b.opts.Queue, err)
	}

	if err := ch.Qos(prefetch, 0, false); err != nil {
		return nil, fmt.Errorf("setting prefetch: %w", err)
	}

	deliveries, err := ch.ConsumeWithContext(ctx, b.opts.Queue, b.opts.Name, false, false, false, false, nil)
	if err != nil {
		return nil, fmt.Errorf("consuming %q: %w", b.opts.Queue, err)
	}

	pub, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("opening publisher channel: %w", err)
	}

	if err := pub.Confirm(false); err != nil {
		return nil, fmt.Errorf("enabling publisher confirms: %w", err)
	}

	b.setPublisher(pub)

	return deliveries, nil
}

func (b *Bus) setPublisher(pub *amqp.Channel) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.pub = pub
}

// deliver hands d to handle and acknowledges it. Only a failed
// acknowledgement is returned: the channel is gone then.
func (b *Bus) deliver(ctx context.Context, d amqp.Delivery, handle Handler) error {
	l, err := Decode(d.Body)
	if err == nil {
		err = handle(ctx, l)
	}

	switch {
	case err == nil:
		return ack(d.Ack(false))
	case errors.Is(err, logout.ErrInvalid) || l.ID == "":
		slog.WarnContext(ctx, "dropped an invalid logout message", "message_id", d.MessageId, "error", err)
		return ack(d.Reject(false))
	default:
		slog.WarnContext(ctx, "failed to handle a logout message, it is delivered again", "logout_id", l.ID, "error", err)

		select {
		case <-ctx.Done():
		case <-time.After(retryDelay):
		}

		return ack(d.Nack(false, true))
	}
}

func ack(err error) error {
	if err != nil {
		return fmt.Errorf("acknowledging a logout message: %w", err)
	}

	return nil
}
