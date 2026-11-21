package store

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestEscapeLikeAndHouseNumberText(t *testing.T) {
	t.Parallel()

	a, blank := " a ", ""

	tests := []struct {
		name, got, want string
	}{
		{"plain number", HouseNumberText(12, nil), "12"},
		{"addition trimmed", HouseNumberText(12, &a), "12a"},
		{"empty addition", HouseNumberText(3, &blank), "3"},
		{"wildcards escaped", escapeLike(`50%_\`), `50\%\_\\`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.got != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

const (
	testCountry   = "Germany"
	districtMitte = "Mitte"
	districtNord  = "Nord"
)

// suggestFixture holds streets in cities named after a tag, so the
// suggestions of each test are its own rows only.
type suggestFixture struct {
	tag     string
	cityA   string
	cityB   string
	suggest *SuggestStore
	streets []BulkResult
}

func newSuggestFixture(t *testing.T) suggestFixture {
	t.Helper()

	db := openBenchDB(t)
	tag := uuid.NewString()[:8]
	cityA, cityB := "Sugcity "+tag, "Othercity "+tag

	street := func(city, district, name string, numbers ...BulkHouseNumberInput) BulkStreetInput {
		return BulkStreetInput{
			City: city, District: district, Name: name, Country: testCountry,
			Latitude: 50, Longitude: 7, HouseNumbers: numbers,
		}
	}
	num := func(n int, addition string) BulkHouseNumberInput {
		h := BulkHouseNumberInput{Number: n, Postcode: "53111", Latitude: 50, Longitude: 7}
		if addition != "" {
			h.NumberAddition = &addition
		}

		return h
	}

	streets := createBulk(t, db, []BulkStreetInput{
		// "Alte Hauptstraße" contains the name but does not start with it.
		street(cityA, districtMitte, "Alte Hauptstraße "+tag, num(1, "")),
		street(cityA, districtMitte, "Hauptstraße "+tag, num(10, ""), num(2, "b"), num(2, "a"), num(2, "")),
		// The same name in another district: its numbers are merged in.
		street(cityA, districtNord, "Hauptstraße "+tag, num(2, "A"), num(5, "")),
		street(cityB, "Süd", "Hauptstraße "+tag, num(7, "")),
		street(cityA, districtMitte, "Haus_weg "+tag),
	})

	return suggestFixture{tag: tag, cityA: cityA, cityB: cityB, suggest: NewSuggestStore(db), streets: streets}
}

func TestSuggestStore_Streets(t *testing.T) {
	t.Parallel()

	f := newSuggestFixture(t)

	type row struct{ name, district, city string }

	tests := []struct {
		name string
		q    SuggestQuery
		want []row
	}{
		{"too short", SuggestQuery{Name: "H", City: f.cityA}, nil},
		{
			"prefix first, then by name, city and district",
			SuggestQuery{Name: " haupt ", City: f.cityA},
			[]row{
				{"Hauptstraße " + f.tag, districtMitte, f.cityA},
				{"Hauptstraße " + f.tag, districtNord, f.cityA},
				{"Alte Hauptstraße " + f.tag, districtMitte, f.cityA},
			},
		},
		{"district filter", SuggestQuery{Name: "Haupt", City: f.cityA, District: "nord"}, []row{{"Hauptstraße " + f.tag, districtNord, f.cityA}}},
		{"wildcards are literal", SuggestQuery{Name: "s_w", City: f.cityA}, []row{{"Haus_weg " + f.tag, districtMitte, f.cityA}}},
		{"percent matches nothing", SuggestQuery{Name: "%%", City: f.cityA}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := f.suggest.Streets(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}

			var rows []row
			for _, s := range got {
				rows = append(rows, row{s.Name, s.District, s.City})
			}

			if !slices.Equal(rows, tt.want) {
				t.Fatalf("got %v, want %v", rows, tt.want)
			}
		})
	}
}

func TestSuggestStore_HouseNumbers(t *testing.T) {
	t.Parallel()

	f := newSuggestFixture(t)

	tests := []struct {
		name string
		q    SuggestQuery
		want []string
	}{
		{"no street", SuggestQuery{City: f.cityA}, nil},
		{
			// 2A of Nord and 2a of Mitte are the same number.
			"exact name, merged across districts, numeric order, each once",
			SuggestQuery{Street: "hauptstraße " + f.tag, City: f.cityA},
			[]string{"2", "2a", "2b", "5", "10"},
		},
		{"district filter", SuggestQuery{Street: "Hauptstraße " + f.tag, City: f.cityA, District: districtNord}, []string{"2A", "5"}},
		{"other city", SuggestQuery{Street: "Hauptstraße " + f.tag, City: f.cityB}, []string{"7"}},
		{"substring is not the street", SuggestQuery{Street: "straße " + f.tag, City: f.cityA}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := f.suggest.HouseNumbers(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}

			var numbers []string
			for _, n := range got {
				numbers = append(numbers, n.Number)
			}

			// Which street's copy of a number is kept depends on their IDs.
			if !slices.EqualFunc(numbers, tt.want, strings.EqualFold) {
				t.Fatalf("got %v, want %v", numbers, tt.want)
			}
		})
	}
}

func TestSuggestStore_CitiesAndDistricts(t *testing.T) {
	t.Parallel()

	f := newSuggestFixture(t)

	cities, err := f.suggest.Cities(t.Context(), SuggestQuery{Name: f.tag})
	if err != nil {
		t.Fatal(err)
	}

	// Neither starts with the tag, so by name.
	if want := []string{f.cityB, f.cityA}; !slices.Equal(cities, want) {
		t.Errorf("Cities = %v, want %v", cities, want)
	}

	cities, err = f.suggest.Cities(t.Context(), SuggestQuery{Name: "sugcity " + f.tag})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{f.cityA}; !slices.Equal(cities, want) {
		t.Errorf("Cities(prefix) = %v, want %v", cities, want)
	}

	districts, err := f.suggest.Districts(t.Context(), SuggestQuery{City: f.cityA})
	if err != nil {
		t.Fatal(err)
	}

	if want := []District{{districtMitte, f.cityA}, {districtNord, f.cityA}}; !slices.Equal(districts, want) {
		t.Errorf("Districts = %v, want %v", districts, want)
	}

	districts, err = f.suggest.Districts(t.Context(), SuggestQuery{Name: "it", City: f.cityA})
	if err != nil {
		t.Fatal(err)
	}

	if want := []District{{districtMitte, f.cityA}}; !slices.Equal(districts, want) {
		t.Errorf("Districts(name) = %v, want %v", districts, want)
	}
}

func TestSuggestStore_GeoObjects(t *testing.T) {
	t.Parallel()

	f := newSuggestFixture(t)
	geo := NewGeoObjectStore(f.suggest.db)
	mitte := f.streets[1]

	create := func(name string, street BulkResult, houseNumber int) {
		g := GeoObject{Name: name, Group: "Hydrants " + f.tag, StreetID: street.Street.ID, Latitude: 50.1, Longitude: 7.1}
		if houseNumber >= 0 {
			g.HouseNumberID = &street.HouseNumbers[houseNumber].ID
		}

		created, err := geo.Create(t.Context(), g)
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { _ = geo.Delete(t.Context(), created.ID) })
	}

	create("Hydrant 2 "+f.tag, mitte, 2) // number 2a
	create("Alter Hydrant "+f.tag, mitte, -1)
	create("Hydrant 9 "+f.tag, f.streets[3], -1) // in cityB

	tests := []struct {
		name string
		q    SuggestQuery
		want []string
	}{
		{"too short", SuggestQuery{Name: "H"}, nil},
		{
			"prefix first, address joined",
			SuggestQuery{Name: "hydrant", City: f.cityA},
			[]string{
				fmt.Sprintf("Hydrant 2 %s|Hauptstraße %s|2a|53111|Mitte", f.tag, f.tag),
				fmt.Sprintf("Alter Hydrant %s|Hauptstraße %s|||Mitte", f.tag, f.tag),
			},
		},
		{"city filter", SuggestQuery{Name: "Hydrant 9 " + f.tag, City: f.cityB}, []string{
			fmt.Sprintf("Hydrant 9 %s|Hauptstraße %s|||Süd", f.tag, f.tag),
		}},
		{"other city filtered out", SuggestQuery{Name: "Hydrant 9 " + f.tag, City: f.cityA}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := f.suggest.GeoObjects(t.Context(), tt.q)
			if err != nil {
				t.Fatal(err)
			}

			var rows []string

			for _, g := range got {
				if g.Group != "Hydrants "+f.tag {
					continue // seed data matching "hydrant"
				}

				rows = append(rows, fmt.Sprintf("%s|%s|%s|%s|%s", g.Name, g.Street, g.HouseNumber, g.Postcode, g.District))
			}

			if !slices.Equal(rows, tt.want) {
				t.Fatalf("got %v, want %v", rows, tt.want)
			}
		})
	}
}
