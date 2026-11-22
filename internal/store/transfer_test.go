package store

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The streets of transferDoc and of ownershipFixture.
const (
	manualStreet = "Manual"
	ownedStreet  = "Owned"
)

// summary is how summarize lists street and its children.
func summary(street string, children ...string) []string {
	out := []string{street}
	for _, c := range children {
		out = append(out, street+"/"+c)
	}

	return out
}

// transferDoc is a document with a manual street holding a geo object of
// collector "c1" at a manual house number, and a street of collector "c2".
func transferDoc() Export {
	c1, c2 := "c1", "c2"

	return Export{FormatVersion: ExportFormatVersion, Streets: []ExportStreet{
		{
			Name: manualStreet, District: "D", City: "C", Country: "XX", Latitude: 50, Longitude: 7,
			HouseNumbers: []ExportHouseNumber{
				{Number: 1, Postcode: "11111", Latitude: 50, Longitude: 7},
				{Number: 2, Postcode: "11111", Latitude: 50, Longitude: 7},
			},
			GeoObjects: []ExportGeoObject{
				{Name: "h1", Group: "hydrant", HouseNumber: &ExportHouseNumberRef{Number: 1}, Latitude: 50, Longitude: 7, CollectorID: &c1},
				{Name: "h2", Group: "hydrant", Latitude: 50, Longitude: 7},
			},
		},
		{
			Name: ownedStreet, District: "D", City: "C", Country: "XX", Latitude: 50, Longitude: 7, CollectorID: &c2,
			HouseNumbers: []ExportHouseNumber{{Number: 1, Postcode: "11111", Latitude: 50, Longitude: 7, CollectorID: &c2}},
			GeoObjects:   []ExportGeoObject{},
		},
	}}
}

// summarize lists e's rows as "street", "street/number" and "street/name".
func summarize(e Export) []string {
	var out []string

	for _, s := range e.Streets {
		out = append(out, s.Name)

		for _, h := range s.HouseNumbers {
			out = append(out, s.Name+"/"+strconv.Itoa(h.Number))
		}

		for _, g := range s.GeoObjects {
			out = append(out, s.Name+"/"+g.Name)
		}
	}

	return out
}

func TestExportFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		filter SourceFilter
		want   []string
	}{
		{"everything", SourceFilter{}, slices.Concat(summary(manualStreet, "1", "2", "h1", "h2"), summary(ownedStreet, "1"))},
		{"manual rows", SourceFilter{Manual: true}, summary(manualStreet, "1", "2", "h2")},
		// h1 needs its street and house number, though they are manual.
		{"one collector", SourceFilter{Collectors: []string{"c1"}}, summary(manualStreet, "1", "h1")},
		{"other collector", SourceFilter{Collectors: []string{"c2"}}, summary(ownedStreet, "1")},
		{"manual rows and a collector", SourceFilter{Manual: true, Collectors: []string{"c2"}}, slices.Concat(summary(manualStreet, "1", "2", "h2"), summary(ownedStreet, "1"))},
		{"unknown collector", SourceFilter{Collectors: []string{"nope"}}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := summarize(transferDoc().Filter(tt.filter)); !slices.Equal(got, tt.want) {
				t.Errorf("Filter = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestExportValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Export)
		wantErr string
		is      error
	}{
		{name: "valid", mutate: func(*Export) {}},
		{name: "wrong version", mutate: func(e *Export) { e.FormatVersion = 2 }, wantErr: "formatVersion"},
		{name: "street without name", mutate: func(e *Export) { e.Streets[0].Name = "" }, wantErr: "streets[0]"},
		{name: "repeated street", mutate: func(e *Export) { e.Streets[1].Name = manualStreet }, is: ErrStreetAlreadyExists},
		{name: "invalid house number", mutate: func(e *Export) { e.Streets[0].HouseNumbers[1].Postcode = "" }, wantErr: "houseNumbers[1]"},
		{name: "repeated house number", mutate: func(e *Export) { e.Streets[0].HouseNumbers[1].Number = 1 }, is: ErrHouseNumberAlreadyExists},
		{name: "geo object without group", mutate: func(e *Export) { e.Streets[0].GeoObjects[1].Group = "" }, wantErr: "geoObjects[1]"},
		{name: "geo object at unlisted house number", mutate: func(e *Export) {
			e.Streets[0].GeoObjects[0].HouseNumber = &ExportHouseNumberRef{Number: 9}
		}, is: ErrHouseNumberNotInExport},
		{name: "repeated geo object name", mutate: func(e *Export) { e.Streets[0].GeoObjects[1].Name = "h1" }, is: ErrNameTaken},
		{name: "repeated external ID across streets", mutate: func(e *Export) {
			e.Streets[0].GeoObjects[0].ExternalID = new("x")
			e.Streets[1].GeoObjects = []ExportGeoObject{{Name: "o", Group: "g", Latitude: 1, Longitude: 1, ExternalID: new("x")}}
		}, is: ErrExternalIDTaken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc := transferDoc()
			tt.mutate(&doc)
			err := doc.Validate()

			switch {
			case tt.is != nil:
				if !errors.Is(err, tt.is) {
					t.Errorf("Validate = %v, want %v", err, tt.is)
				}
			case tt.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Validate = %v, want an error mentioning %q", err, tt.wantErr)
				}
			case err != nil:
				t.Errorf("Validate = %v", err)
			}
		})
	}
}
