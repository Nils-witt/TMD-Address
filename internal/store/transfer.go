package store

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
)

// ExportFormatVersion is the version of the Export document layout. Import
// refuses documents of other versions.
const ExportFormatVersion = 1

// Export is a portable copy of streets with their house numbers and geo
// objects. Rows refer to each other by their natural keys, not by ID, so a
// document exported from one instance can be imported into another.
type Export struct {
	FormatVersion int            `json:"formatVersion"`
	ExportedAt    time.Time      `json:"exportedAt"`
	Streets       []ExportStreet `json:"streets"`
}

// ExportStreet is a street of an Export, keyed by name, district, city and
// country.
type ExportStreet struct {
	Name      string  `json:"name"`
	District  string  `json:"district"`
	City      string  `json:"city"`
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	// CollectorID names the collector that owned the street when it was
	// exported, nil for a manual one. Import uses it only to filter.
	CollectorID  *string             `json:"collectorId,omitempty"`
	HouseNumbers []ExportHouseNumber `json:"houseNumbers"`
	GeoObjects   []ExportGeoObject   `json:"geoObjects"`
}

// ExportHouseNumber is a house number of an ExportStreet, keyed by number and
// addition.
type ExportHouseNumber struct {
	Number         int     `json:"number"`
	NumberAddition *string `json:"numberAddition,omitempty"`
	Postcode       string  `json:"postcode"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	CollectorID    *string `json:"collectorId,omitempty"`
}

// ExportHouseNumberRef names a house number of the same ExportStreet.
type ExportHouseNumberRef struct {
	Number         int     `json:"number"`
	NumberAddition *string `json:"numberAddition,omitempty"`
}

// ExportGeoObject is a geo object of an ExportStreet, keyed by its external
// ID if it has one, else by group and name.
type ExportGeoObject struct {
	Name        string                `json:"name"`
	Group       string                `json:"group"`
	HouseNumber *ExportHouseNumberRef `json:"houseNumber,omitempty"`
	Latitude    float64               `json:"latitude"`
	Longitude   float64               `json:"longitude"`
	ExternalID  *string               `json:"externalId,omitempty"`
	CollectorID *string               `json:"collectorId,omitempty"`
}

// SourceFilter selects rows by who owns them: manual rows, rows of the named
// collectors, or both. The zero SourceFilter selects every row.
type SourceFilter struct {
	Manual     bool
	Collectors []string
}

// IsZero reports whether f selects every row.
func (f SourceFilter) IsZero() bool {
	return !f.Manual && len(f.Collectors) == 0
}

// matches reports whether f selects a row owned by collectorID, nil for a
// manual row.
func (f SourceFilter) matches(collectorID *string) bool {
	if f.IsZero() {
		return true
	}

	if collectorID == nil {
		return f.Manual
	}

	return slices.Contains(f.Collectors, *collectorID)
}

// Filter returns the part of e that f selects. A row that a selected row
// needs comes along even if f doesn't select it: the street of a selected
// house number or geo object, and the house number a selected geo object is
// at. Without them the selected rows could not be imported elsewhere.
func (e Export) Filter(f SourceFilter) Export {
	if f.IsZero() {
		return e
	}

	out := Export{FormatVersion: e.FormatVersion, ExportedAt: e.ExportedAt, Streets: []ExportStreet{}}

	for _, s := range e.Streets {
		if kept, ok := s.filter(f); ok {
			out.Streets = append(out.Streets, kept)
		}
	}

	return out
}

// filter returns the part of s that f selects, and whether anything is left.
func (s ExportStreet) filter(f SourceFilter) (ExportStreet, bool) {
	kept := s
	kept.GeoObjects = []ExportGeoObject{}
	needed := map[houseNumberKey]bool{}

	for _, g := range s.GeoObjects {
		if !f.matches(g.CollectorID) {
			continue
		}

		kept.GeoObjects = append(kept.GeoObjects, g)

		if g.HouseNumber != nil {
			needed[g.HouseNumber.key()] = true
		}
	}

	kept.HouseNumbers = []ExportHouseNumber{}

	for _, h := range s.HouseNumbers {
		if f.matches(h.CollectorID) || needed[h.key()] {
			kept.HouseNumbers = append(kept.HouseNumbers, h)
		}
	}

	ok := f.matches(s.CollectorID) || len(kept.HouseNumbers) > 0 || len(kept.GeoObjects) > 0

	return kept, ok
}

// houseNumberKey is a house number's number and addition, unique on its
// street; a nil addition counts as an empty one, as in the unique index.
type houseNumberKey struct {
	number   int
	addition string
}

func newHouseNumberKey(number int, addition *string) houseNumberKey {
	k := houseNumberKey{number: number}
	if addition != nil {
		k.addition = *addition
	}

	return k
}

func (h ExportHouseNumber) key() houseNumberKey {
	return newHouseNumberKey(h.Number, h.NumberAddition)
}

func (r ExportHouseNumberRef) key() houseNumberKey {
	return newHouseNumberKey(r.Number, r.NumberAddition)
}

type geoObjectNameKey struct {
	group, name string
}

// Validate reports whether e can be imported: whether it has the current
// format version, every row is valid, no key repeats, and every geo object's
// house number is one of its street's house numbers.
func (e Export) Validate() error {
	if e.FormatVersion != ExportFormatVersion {
		return fmt.Errorf("formatVersion must be %d", ExportFormatVersion)
	}

	streets := make(map[streetKey]struct{}, len(e.Streets))
	names := map[geoObjectNameKey]struct{}{}
	externalIDs := map[string]struct{}{}

	for i, s := range e.Streets {
		key := streetKey{s.Name, s.District, s.City, s.Country}
		if _, dup := streets[key]; dup {
			return fmt.Errorf("streets[%d]: %w", i, ErrStreetAlreadyExists)
		}

		streets[key] = struct{}{}

		if err := s.validate(names, externalIDs); err != nil {
			return fmt.Errorf("streets[%d]: %w", i, err)
		}
	}

	return nil
}

// validate checks s and its rows. names and externalIDs collect the geo
// object keys seen so far, which must be unique across all streets.
func (s ExportStreet) validate(names map[geoObjectNameKey]struct{}, externalIDs map[string]struct{}) error {
	street := Street{City: s.City, District: s.District, Name: s.Name, Country: s.Country, Latitude: s.Latitude, Longitude: s.Longitude}
	if err := street.Validate(); err != nil {
		return err
	}

	numbers := make(map[houseNumberKey]struct{}, len(s.HouseNumbers))

	for i, h := range s.HouseNumbers {
		in := BulkHouseNumberInput{Number: h.Number, Postcode: h.Postcode, Latitude: h.Latitude, Longitude: h.Longitude}
		if err := in.Validate(); err != nil {
			return fmt.Errorf("houseNumbers[%d]: %w", i, err)
		}

		if _, dup := numbers[h.key()]; dup {
			return fmt.Errorf("houseNumbers[%d]: %w", i, ErrHouseNumberAlreadyExists)
		}

		numbers[h.key()] = struct{}{}
	}

	for i, g := range s.GeoObjects {
		if err := g.validate(numbers, names, externalIDs); err != nil {
			return fmt.Errorf("geoObjects[%d]: %w", i, err)
		}
	}

	return nil
}

// ErrHouseNumberNotInExport is returned when a geo object of an Export is at
// a house number its street doesn't list.
var ErrHouseNumberNotInExport = errors.New("houseNumber must be one of the street's houseNumbers")

// placeholderID stands in for the street ID of an ExportGeoObject while it is
// validated as a GeoObject; the street is the enclosing ExportStreet.
var placeholderID = uuid.Max

func (g ExportGeoObject) validate(
	numbers map[houseNumberKey]struct{},
	names map[geoObjectNameKey]struct{},
	externalIDs map[string]struct{},
) error {
	row := GeoObject{Name: g.Name, Group: g.Group, StreetID: placeholderID, Latitude: g.Latitude, Longitude: g.Longitude, ExternalID: g.ExternalID}
	if err := row.Validate(); err != nil {
		return err
	}

	if g.HouseNumber != nil {
		if _, ok := numbers[g.HouseNumber.key()]; !ok {
			return ErrHouseNumberNotInExport
		}
	}

	name := geoObjectNameKey{g.Group, g.Name}
	if _, dup := names[name]; dup {
		return ErrNameTaken
	}

	names[name] = struct{}{}

	if g.ExternalID != nil {
		if _, dup := externalIDs[*g.ExternalID]; dup {
			return ErrExternalIDTaken
		}

		externalIDs[*g.ExternalID] = struct{}{}
	}

	return nil
}
