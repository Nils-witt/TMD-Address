package store_test

import (
	"testing"
	"time"

	"github.com/nils-witt/tmd-address/internal/logout"
	"github.com/nils-witt/tmd-address/internal/store"
	"github.com/nils-witt/tmd-address/internal/store/storetest"
)

func TestProviderLogoutStore(t *testing.T) {
	t.Parallel()

	s := store.NewProviderLogoutStore(storetest.OpenIsolated(t))
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Microsecond)
	l := logout.Logout{ID: "l1", Issuer: "https://idp/", Subject: "alice", SID: "s1", IssuedAt: now, Origin: "address"}
	received := logout.Logout{ID: "l2", Issuer: "https://idp/", SID: "s2", IssuedAt: now.Add(-48 * time.Hour), Origin: "api"}

	if added, err := s.AddProviderLogout(ctx, l, true); err != nil || !added {
		t.Fatalf("AddProviderLogout = %v, %v", added, err)
	}

	if added, err := s.AddProviderLogout(ctx, l, true); err != nil || added {
		t.Fatalf("AddProviderLogout again = %v, %v; want not added", added, err)
	}

	if _, err := s.AddProviderLogout(ctx, received, false); err != nil {
		t.Fatal(err)
	}

	assertOutbox(t, s, l)

	if list, err := s.ProviderLogoutsSince(ctx, now.Add(-time.Hour)); err != nil || len(list) != 1 || list[0].ID != "l1" {
		t.Fatalf("ProviderLogoutsSince = %+v, %v", list, err)
	}

	if err := s.DeleteProviderLogoutsBefore(ctx, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	if list, err := s.ProviderLogoutsSince(ctx, time.Time{}); err != nil || len(list) != 1 {
		t.Fatalf("after DeleteProviderLogoutsBefore: %+v, %v", list, err)
	}
}

// assertOutbox checks that only l is still to announce, until it is marked
// announced.
func assertOutbox(t *testing.T, s *store.ProviderLogoutStore, l logout.Logout) {
	t.Helper()

	pending, err := s.UnannouncedProviderLogouts(t.Context())
	if err != nil || len(pending) != 1 {
		t.Fatalf("UnannouncedProviderLogouts = %+v, %v", pending, err)
	}

	if got := pending[0]; got.ID != l.ID || !got.IssuedAt.Equal(l.IssuedAt) || got.Subject != l.Subject || got.SID != l.SID || got.Origin != l.Origin {
		t.Fatalf("unannounced %+v, want %+v", got, l)
	}

	if err := s.MarkProviderLogoutAnnounced(t.Context(), l.ID); err != nil {
		t.Fatal(err)
	}

	if pending, err := s.UnannouncedProviderLogouts(t.Context()); err != nil || len(pending) != 0 {
		t.Fatalf("after marking announced: %+v, %v", pending, err)
	}
}
