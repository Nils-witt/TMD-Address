package logout

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DefaultRetention is how long a logout is remembered: longer than any access
// token it revokes lives.
const DefaultRetention = 24 * time.Hour

// syncInterval is how often the logouts other instances stored are loaded, and
// the unannounced ones announced again.
const syncInterval = 5 * time.Second

// Store keeps the logouts, so they outlive a restart and reach the other
// instances, and is the outbox of those to announce (see store.ProviderLogoutStore).
type Store interface {
	// AddProviderLogout stores l unless its ID is known; announce marks it to
	// be announced. It reports whether l was new.
	AddProviderLogout(ctx context.Context, l Logout, announce bool) (bool, error)
	ProviderLogoutsSince(ctx context.Context, t time.Time) ([]Logout, error)
	UnannouncedProviderLogouts(ctx context.Context) ([]Logout, error)
	MarkProviderLogoutAnnounced(ctx context.Context, id string) error
	DeleteProviderLogoutsBefore(ctx context.Context, t time.Time) error
}

// Broadcaster announces logouts to the other services. Publish returns once
// the broker accepted l.
type Broadcaster interface {
	Publish(ctx context.Context, l Logout) error
}

// Deps are the dependencies of a Service.
type Deps struct {
	Store Store
	// Broadcast is nil when logouts are not shared with other services.
	Broadcast Broadcaster
	// Retention is how long logouts are kept; zero is DefaultRetention.
	Retention time.Duration
}

// Service applies logouts and tells which access tokens they revoked. It is
// safe for concurrent use. A nil *Service revokes nothing.
type Service struct {
	d   Deps
	now func() time.Time
	// announce wakes Run when a logout is to be announced.
	announce chan struct{}

	mu      sync.RWMutex
	logouts map[string]Logout
}

// NewService creates a Service. Call Load before serving requests, and Run to
// keep it in sync.
func NewService(d Deps) *Service {
	if d.Retention <= 0 {
		d.Retention = DefaultRetention
	}

	return &Service{d: d, now: time.Now, announce: make(chan struct{}, 1), logouts: map[string]Logout{}}
}

// BackchannelLogout applies a logout this service received from the provider
// and queues it for the other services.
func (s *Service) BackchannelLogout(ctx context.Context, l Logout) error {
	l.Origin = Origin

	announce := s.d.Broadcast != nil
	if err := s.apply(ctx, l, announce); err != nil {
		return err
	}

	if announce {
		select {
		case s.announce <- struct{}{}:
		default:
		}
	}

	return nil
}

// Receive applies a logout another service (or replica) announced. One that
// is malformed wraps ErrInvalid.
func (s *Service) Receive(ctx context.Context, l Logout) error {
	if l.ID == "" || (l.Subject == "" && l.SID == "") || l.IssuedAt.IsZero() {
		return fmt.Errorf("%w: a logout needs an id, a subject or sid, and its time", ErrInvalid)
	}

	if l.IssuedAt.Before(s.now().Add(-s.d.Retention)) {
		return nil // anything it revokes has expired
	}

	return s.apply(ctx, l, false)
}

func (s *Service) apply(ctx context.Context, l Logout, announce bool) error {
	added, err := s.d.Store.AddProviderLogout(ctx, l, announce)
	if err != nil {
		return fmt.Errorf("store logout: %w", err)
	}

	s.remember(l)

	if added {
		slog.InfoContext(ctx, "provider logout applied", "logout_id", l.ID, "origin", l.Origin, "by_sid", l.SID != "")
	}

	return nil
}

func (s *Service) remember(l Logout) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.logouts[l.ID] = l
}

// Revoked reports whether a logout revoked an access token of subject in the
// provider session sid issued at issuedAt.
func (s *Service) Revoked(subject, sid string, issuedAt time.Time) bool {
	if s == nil || subject == "" {
		return false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, l := range s.logouts {
		if l.RevokesToken(subject, sid, issuedAt) {
			return true
		}
	}

	return false
}

// Load reads the logouts still in effect from the store.
func (s *Service) Load(ctx context.Context) error {
	cutoff := s.now().Add(-s.d.Retention)

	list, err := s.d.Store.ProviderLogoutsSince(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("load logouts: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, l := range s.logouts {
		if l.IssuedAt.Before(cutoff) {
			delete(s.logouts, id)
		}
	}

	for _, l := range list {
		s.logouts[l.ID] = l
	}

	return nil
}

// Run keeps the logouts in sync until ctx is done: it loads those the other
// instances stored, forgets expired ones and announces the unannounced. It is
// meant to run in its own goroutine.
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(syncInterval)
	defer tick.Stop()

	for {
		if err := s.Load(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "failed to load provider logouts", "error", err)
		}

		s.announceAll(ctx)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			s.prune(ctx)
		case <-s.announce:
		}
	}
}

func (s *Service) prune(ctx context.Context) {
	err := s.d.Store.DeleteProviderLogoutsBefore(ctx, s.now().Add(-s.d.Retention))
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "failed to delete expired provider logouts", "error", err)
	}
}

// announceAll publishes the unannounced logouts. One that fails stays
// unannounced, to be tried again on the next run.
func (s *Service) announceAll(ctx context.Context) {
	if s.d.Broadcast == nil {
		return
	}

	list, err := s.d.Store.UnannouncedProviderLogouts(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "failed to load unannounced provider logouts", "error", err)
		}

		return
	}

	for _, l := range list {
		if err := s.d.Broadcast.Publish(ctx, l); err != nil {
			if ctx.Err() == nil && !errors.Is(err, ErrBrokerUnavailable) {
				slog.WarnContext(ctx, "failed to announce a provider logout", "logout_id", l.ID, "error", err)
			}

			return
		}

		if err := s.d.Store.MarkProviderLogoutAnnounced(ctx, l.ID); err != nil {
			slog.WarnContext(ctx, "failed to record an announced provider logout", "logout_id", l.ID, "error", err)

			return
		}
	}
}
