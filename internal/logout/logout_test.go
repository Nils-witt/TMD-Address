package logout

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

const (
	alice = "alice"
	bob   = "bob"
)

func TestRevokesToken(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	bySubject := Logout{Subject: alice, IssuedAt: at}
	bySID := Logout{SID: "s1", IssuedAt: at}
	byBoth := Logout{Subject: alice, SID: "s1", IssuedAt: at}
	before := at.Add(-time.Minute)

	tests := []struct {
		name     string
		logout   Logout
		subject  string
		sid      string
		issuedAt time.Time
		want     bool
	}{
		{name: "subject, issued before", logout: bySubject, subject: alice, sid: "s9", issuedAt: before, want: true},
		{name: "subject, issued at the same time", logout: bySubject, subject: alice, issuedAt: at, want: true},
		{name: "subject, issued after", logout: bySubject, subject: alice, issuedAt: at.Add(time.Second)},
		{name: "other subject", logout: bySubject, subject: bob, issuedAt: before},
		{name: "subject, no iat", logout: bySubject, subject: alice, want: true},
		{name: "sid matches", logout: bySID, subject: bob, sid: "s1", issuedAt: before, want: true},
		{name: "other sid", logout: bySID, subject: bob, sid: "s2", issuedAt: before},
		{name: "sid logout, token without sid", logout: bySID, subject: bob, issuedAt: before},
		{name: "both match", logout: byBoth, subject: alice, sid: "s1", issuedAt: before, want: true},
		{name: "both, other session of the subject", logout: byBoth, subject: alice, sid: "s2", issuedAt: before},
		{name: "both, same sid of another subject", logout: byBoth, subject: bob, sid: "s1", issuedAt: before},
		{name: "both, token without sid falls back to subject", logout: byBoth, subject: alice, issuedAt: before, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := tc.logout.RevokesToken(tc.subject, tc.sid, tc.issuedAt); got != tc.want {
				t.Errorf("RevokesToken(%q, %q, %v) = %v, want %v", tc.subject, tc.sid, tc.issuedAt, got, tc.want)
			}
		})
	}
}

type fakeStore struct {
	mu        sync.Mutex
	logouts   map[string]Logout
	announced map[string]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{logouts: map[string]Logout{}, announced: map[string]bool{}}
}

func (f *fakeStore) AddProviderLogout(_ context.Context, l Logout, announce bool) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.logouts[l.ID]; ok {
		return false, nil
	}

	f.logouts[l.ID] = l
	f.announced[l.ID] = !announce

	return true, nil
}

func (f *fakeStore) ProviderLogoutsSince(_ context.Context, t time.Time) ([]Logout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Logout

	for _, l := range f.logouts {
		if l.IssuedAt.After(t) {
			out = append(out, l)
		}
	}

	return out, nil
}

func (f *fakeStore) UnannouncedProviderLogouts(context.Context) ([]Logout, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []Logout

	for id, done := range f.announced {
		if !done {
			out = append(out, f.logouts[id])
		}
	}

	return out, nil
}

func (f *fakeStore) MarkProviderLogoutAnnounced(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.announced[id] = true

	return nil
}

func (f *fakeStore) DeleteProviderLogoutsBefore(context.Context, time.Time) error { return nil }

func (f *fakeStore) isAnnounced(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.announced[id]
}

type fakeBroadcaster struct {
	mu        sync.Mutex
	published int
	err       error
}

func (f *fakeBroadcaster) Publish(context.Context, Logout) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}

	f.published++

	return nil
}

func TestServiceBackchannelLogout(t *testing.T) {
	t.Parallel()

	at := time.Now().Add(-time.Minute)
	st, bus := newFakeStore(), &fakeBroadcaster{}
	s := NewService(Deps{Store: st, Broadcast: bus})

	if err := s.BackchannelLogout(t.Context(), Logout{ID: "l1", Subject: "sub-a", IssuedAt: at}); err != nil {
		t.Fatal(err)
	}

	if st.logouts["l1"].Origin != Origin {
		t.Errorf("stored %+v, want origin %q", st.logouts["l1"], Origin)
	}

	if !s.Revoked("sub-a", "", at.Add(-time.Second)) || s.Revoked("sub-a", "", at.Add(time.Second)) || s.Revoked("sub-b", "", at) {
		t.Error("Revoked does not follow the logout")
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		s.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !st.isAnnounced("l1") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-done

	if !st.isAnnounced("l1") || bus.published != 1 {
		t.Errorf("announced %v, published %d times", st.isAnnounced("l1"), bus.published)
	}
}

func TestServiceKeepsUnannouncedWhileBrokerIsDown(t *testing.T) {
	t.Parallel()

	st := newFakeStore()
	s := NewService(Deps{Store: st, Broadcast: &fakeBroadcaster{err: ErrBrokerUnavailable}})

	if err := s.BackchannelLogout(t.Context(), Logout{ID: "l1", SID: "s1", IssuedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	s.Run(ctx)

	if st.isAnnounced("l1") {
		t.Error("marked announced although publishing failed")
	}
}

func TestServiceReceive(t *testing.T) {
	t.Parallel()

	now := time.Now()

	tests := []struct {
		name    string
		logout  Logout
		wantErr error
		stored  bool
	}{
		{name: "from another service", logout: Logout{ID: "x", SID: "s1", IssuedAt: now, Origin: "tiles"}, stored: true},
		{name: "expired", logout: Logout{ID: "x", Subject: "a", IssuedAt: now.Add(-48 * time.Hour)}},
		{name: "no id", logout: Logout{Subject: "a", IssuedAt: now}, wantErr: ErrInvalid},
		{name: "no target", logout: Logout{ID: "x", IssuedAt: now}, wantErr: ErrInvalid},
		{name: "no time", logout: Logout{ID: "x", Subject: "a"}, wantErr: ErrInvalid},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			st := newFakeStore()
			s := NewService(Deps{Store: st, Broadcast: &fakeBroadcaster{}})

			if err := s.Receive(t.Context(), tc.logout); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}

			if _, ok := st.logouts["x"]; ok != tc.stored {
				t.Errorf("stored = %v, want %v", ok, tc.stored)
			}

			if tc.stored && !st.isAnnounced("x") {
				t.Error("a received logout would be announced again")
			}
		})
	}
}

func TestNilServiceRevokesNothing(t *testing.T) {
	t.Parallel()

	var s *Service
	if s.Revoked(alice, "", time.Time{}) {
		t.Error("a nil service revoked a token")
	}
}
