package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/nils-witt/tmd-address/internal/logout"
)

// ProviderLogout is a sign-out at the OpenID provider, received by this
// service or announced by another one over the message broker. Rows expire
// (logout.Service prunes them). AnnouncedAt nil marks a logout this service
// received and has still to announce: the table is also the outbox.
type ProviderLogout struct {
	ID          string     `gorm:"primaryKey"`
	Issuer      string     `gorm:"not null"`
	Subject     string     `gorm:"not null;default:''"`
	SID         string     `gorm:"column:sid;not null;default:''"`
	IssuedAt    time.Time  `gorm:"not null;index"`
	Origin      string     `gorm:"not null"`
	ReceivedAt  time.Time  `gorm:"not null"`
	AnnouncedAt *time.Time `gorm:""`
}

func (p ProviderLogout) logout() logout.Logout {
	return logout.Logout{ID: p.ID, Issuer: p.Issuer, Subject: p.Subject, SID: p.SID, IssuedAt: p.IssuedAt, Origin: p.Origin}
}

// ProviderLogoutStore reads and writes provider logouts.
type ProviderLogoutStore struct {
	db *gorm.DB
}

var _ logout.Store = (*ProviderLogoutStore)(nil)

// NewProviderLogoutStore returns a ProviderLogoutStore backed by db.
func NewProviderLogoutStore(db *gorm.DB) *ProviderLogoutStore {
	return &ProviderLogoutStore{db: db}
}

// AddProviderLogout stores l unless its ID is known; announce leaves it to be
// announced. It reports whether l was new.
func (s *ProviderLogoutStore) AddProviderLogout(ctx context.Context, l logout.Logout, announce bool) (bool, error) {
	row := ProviderLogout{
		ID: l.ID, Issuer: l.Issuer, Subject: l.Subject, SID: l.SID, IssuedAt: l.IssuedAt, Origin: l.Origin,
		ReceivedAt: time.Now(),
	}
	if !announce {
		row.AnnouncedAt = &row.ReceivedAt
	}

	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return false, fmt.Errorf("store provider logout: %w", res.Error)
	}

	return res.RowsAffected == 1, nil
}

// ProviderLogoutsSince returns the logouts issued after t.
func (s *ProviderLogoutStore) ProviderLogoutsSince(ctx context.Context, t time.Time) ([]logout.Logout, error) {
	return findProviderLogouts(s.db.WithContext(ctx).Where("issued_at > ?", t))
}

// UnannouncedProviderLogouts returns the logouts still to announce, oldest
// first.
func (s *ProviderLogoutStore) UnannouncedProviderLogouts(ctx context.Context) ([]logout.Logout, error) {
	return findProviderLogouts(s.db.WithContext(ctx).Where("announced_at IS NULL").Order("received_at"))
}

func findProviderLogouts(q *gorm.DB) ([]logout.Logout, error) {
	var rows []ProviderLogout
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list provider logouts: %w", err)
	}

	out := make([]logout.Logout, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.logout())
	}

	return out, nil
}

// MarkProviderLogoutAnnounced records that the logout id was announced.
func (s *ProviderLogoutStore) MarkProviderLogoutAnnounced(ctx context.Context, id string) error {
	err := s.db.WithContext(ctx).Model(&ProviderLogout{}).
		Where("id = ? AND announced_at IS NULL", id).
		Update("announced_at", time.Now()).Error
	if err != nil {
		return fmt.Errorf("mark provider logout announced: %w", err)
	}

	return nil
}

// DeleteProviderLogoutsBefore forgets the logouts issued before t.
func (s *ProviderLogoutStore) DeleteProviderLogoutsBefore(ctx context.Context, t time.Time) error {
	if err := s.db.WithContext(ctx).Where("issued_at < ?", t).Delete(&ProviderLogout{}).Error; err != nil {
		return fmt.Errorf("delete expired provider logouts: %w", err)
	}

	return nil
}
