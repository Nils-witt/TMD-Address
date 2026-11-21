package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"
)

// Bounds of the suggestions, chosen for an address form being typed into:
// more would not be read, and fewer characters would list most of the
// directory.
const (
	// MaxSuggestions bounds the streets, cities, districts and geo objects
	// suggested.
	MaxSuggestions = 25
	// MaxHouseNumberSuggestions bounds the house numbers suggested for a
	// street.
	MaxHouseNumberSuggestions = 500
	// MinSuggestQuery is the length in characters of the shortest street or
	// geo object name searched for.
	MinSuggestQuery = 2
	// maxStreetsForNumbers bounds the streets of the same name, in different
	// districts or cities, whose house numbers are suggested together.
	maxStreetsForNumbers = 5
)

// SuggestQuery is what an address form holds while one of its fields is
// typed into. Name is the text of that field; Street, City and District are
// what is filled in already. Empty fields do not filter.
type SuggestQuery struct {
	Name     string
	Street   string
	City     string
	District string
}

func (q SuggestQuery) trimmed() SuggestQuery {
	return SuggestQuery{
		Name:     strings.TrimSpace(q.Name),
		Street:   strings.TrimSpace(q.Street),
		City:     strings.TrimSpace(q.City),
		District: strings.TrimSpace(q.District),
	}
}

// StreetSuggestion is a street offered for a street field.
type StreetSuggestion struct {
	Name      string  `json:"name"`
	District  string  `json:"district"`
	City      string  `json:"city"`
	Country   string  `json:"country"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// HouseNumberSuggestion is a house number offered for a street.
type HouseNumberSuggestion struct {
	// Number is the number with its addition, e.g. "12a".
	Number    string  `json:"number"`
	Postcode  string  `json:"postcode"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// GeoObjectSuggestion is a geo object with its address. Latitude and
// Longitude are the object's own position.
type GeoObjectSuggestion struct {
	Name   string `json:"name"`
	Group  string `json:"group"`
	Street string `json:"street"`
	// HouseNumber is the number with its addition, empty if the object has
	// none.
	HouseNumber string  `json:"house_number"`
	Postcode    string  `json:"postcode"`
	District    string  `json:"district"`
	City        string  `json:"city"`
	Country     string  `json:"country"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
}

// SuggestStore suggests addresses while an address form is typed into: the
// matches whose name starts with the typed text come first, and every list
// is bounded.
type SuggestStore struct {
	db *gorm.DB
}

// NewSuggestStore returns a SuggestStore backed by db.
func NewSuggestStore(db *gorm.DB) *SuggestStore {
	return &SuggestStore{db: db}
}

// likeArgs are the patterns of a suggestion query: @contains and @prefix for
// the typed name, @city and @district for exact (case-insensitive) matches,
// empty when not filtering.
func likeArgs(q SuggestQuery, limit int) []any {
	name := escapeLike(q.Name)

	return []any{
		sql.Named("contains", "%"+name+"%"),
		sql.Named("prefix", name+"%"),
		sql.Named("street", escapeLike(q.Street)),
		sql.Named("city", escapeLike(q.City)),
		sql.Named("district", escapeLike(q.District)),
		sql.Named("limit", limit),
	}
}

const suggestStreetsQuery = `
SELECT name, district, city, country, latitude, longitude
FROM streets
WHERE name ILIKE @contains
  AND (@city = '' OR city ILIKE @city)
  AND (@district = '' OR district ILIKE @district)
ORDER BY name ILIKE @prefix DESC, lower(name), lower(city), lower(district), id
LIMIT @limit`

// Streets suggests the streets whose name contains q.Name, in q.City and
// q.District when set; none while q.Name is shorter than MinSuggestQuery.
func (st *SuggestStore) Streets(ctx context.Context, q SuggestQuery) ([]StreetSuggestion, error) {
	q = q.trimmed()

	out := []StreetSuggestion{}
	if utf8.RuneCountInString(q.Name) < MinSuggestQuery {
		return out, nil
	}

	err := st.db.WithContext(ctx).Raw(suggestStreetsQuery, likeArgs(q, MaxSuggestions)...).Scan(&out).Error

	return out, err
}

const suggestCitiesQuery = `
SELECT city
FROM streets
WHERE city ILIKE @contains
GROUP BY city
ORDER BY city ILIKE @prefix DESC, lower(city), city
LIMIT @limit`

// Cities suggests the cities whose name contains q.Name, every city while it
// is empty.
func (st *SuggestStore) Cities(ctx context.Context, q SuggestQuery) ([]string, error) {
	out := []string{}
	err := st.db.WithContext(ctx).Raw(suggestCitiesQuery, likeArgs(q.trimmed(), MaxSuggestions)...).Scan(&out).Error

	return out, err
}

const suggestDistrictsQuery = `
SELECT district AS name, city
FROM streets
WHERE district ILIKE @contains
  AND (@city = '' OR city ILIKE @city)
GROUP BY district, city
ORDER BY district ILIKE @prefix DESC, lower(district), lower(city), district, city
LIMIT @limit`

// Districts suggests the districts whose name contains q.Name, in q.City when
// set.
func (st *SuggestStore) Districts(ctx context.Context, q SuggestQuery) ([]District, error) {
	out := []District{}
	err := st.db.WithContext(ctx).Raw(suggestDistrictsQuery, likeArgs(q.trimmed(), MaxSuggestions)...).Scan(&out).Error

	return out, err
}

// suggestHouseNumbersQuery matches the street name in full: a substring
// match would mix in the numbers of longer names ("Hauptstraße" in
// "Alte Hauptstraße"). The same name can be a street in several districts or
// cities; the numbers of the first few are suggested together, as the form
// may not say which one yet.
const suggestHouseNumbersQuery = `
SELECT h.number, h.number_addition, h.postcode, h.latitude, h.longitude
FROM house_numbers h
JOIN (
  SELECT id FROM streets
  WHERE name ILIKE @street
    AND (@city = '' OR city ILIKE @city)
    AND (@district = '' OR district ILIKE @district)
  ORDER BY id
  LIMIT @streets
) s ON s.id = h.street_id
ORDER BY h.number, lower(btrim(coalesce(h.number_addition, ''))), s.id, h.id`

// houseNumberRow is one row of suggestHouseNumbersQuery.
type houseNumberRow struct {
	Number         int
	NumberAddition *string
	Postcode       string
	Latitude       float64
	Longitude      float64
}

// HouseNumberText is n with its addition, e.g. "12a".
func HouseNumberText(n int, addition *string) string {
	s := strconv.Itoa(n)
	if addition != nil {
		s += strings.TrimSpace(*addition)
	}

	return s
}

// HouseNumbers suggests the house numbers of the streets named q.Street,
// ignoring case, in q.City and q.District when set, in numeric order and
// each number once; none without a street.
func (st *SuggestStore) HouseNumbers(ctx context.Context, q SuggestQuery) ([]HouseNumberSuggestion, error) {
	q = q.trimmed()

	out := []HouseNumberSuggestion{}
	if q.Street == "" {
		return out, nil
	}

	var rows []houseNumberRow

	args := append(likeArgs(q, 0), sql.Named("streets", maxStreetsForNumbers))
	if err := st.db.WithContext(ctx).Raw(suggestHouseNumbersQuery, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, r := range rows {
		n := HouseNumberSuggestion{
			Number: HouseNumberText(r.Number, r.NumberAddition), Postcode: r.Postcode,
			Latitude: r.Latitude, Longitude: r.Longitude,
		}

		// Sorted, so the streets' copies of a number are next to each other.
		if len(out) > 0 && strings.EqualFold(out[len(out)-1].Number, n.Number) {
			continue
		}

		if out = append(out, n); len(out) == MaxHouseNumberSuggestions {
			break
		}
	}

	return out, nil
}

const suggestGeoObjectsQuery = `
SELECT g.name, g.group_name, g.latitude, g.longitude,
       s.name AS street, s.district, s.city, s.country,
       h.number AS h_number, h.number_addition AS h_number_addition, h.postcode AS h_postcode
FROM geo_objects g
JOIN streets s ON s.id = g.street_id
LEFT JOIN house_numbers h ON h.id = g.house_number_id
WHERE g.name ILIKE @contains
  AND (@city = '' OR s.city ILIKE @city)
  AND (@district = '' OR s.district ILIKE @district)
ORDER BY g.name ILIKE @prefix DESC, lower(g.name), lower(s.city), g.id
LIMIT @limit`

// geoObjectRow is one row of suggestGeoObjectsQuery; the house number
// columns are nil when the object has none.
type geoObjectRow struct {
	Name            string
	GroupName       string
	Latitude        float64
	Longitude       float64
	Street          string
	District        string
	City            string
	Country         string
	HNumber         *int    `gorm:"column:h_number"`
	HNumberAddition *string `gorm:"column:h_number_addition"`
	HPostcode       *string `gorm:"column:h_postcode"`
}

// GeoObjects suggests the geo objects whose name contains q.Name, on streets
// in q.City and q.District when set, each with its address; none while
// q.Name is shorter than MinSuggestQuery.
func (st *SuggestStore) GeoObjects(ctx context.Context, q SuggestQuery) ([]GeoObjectSuggestion, error) {
	q = q.trimmed()

	out := []GeoObjectSuggestion{}
	if utf8.RuneCountInString(q.Name) < MinSuggestQuery {
		return out, nil
	}

	var rows []geoObjectRow
	if err := st.db.WithContext(ctx).Raw(suggestGeoObjectsQuery, likeArgs(q, MaxSuggestions)...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	for _, r := range rows {
		g := GeoObjectSuggestion{
			Name: r.Name, Group: r.GroupName,
			Street: r.Street, District: r.District, City: r.City, Country: r.Country,
			Latitude: r.Latitude, Longitude: r.Longitude,
		}
		if r.HNumber != nil {
			g.HouseNumber, g.Postcode = HouseNumberText(*r.HNumber, r.HNumberAddition), deref(r.HPostcode)
		}

		out = append(out, g)
	}

	return out, nil
}
