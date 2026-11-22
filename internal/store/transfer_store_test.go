package store

import (
	"slices"
	"testing"
)

// inCity returns the part of e in city.
func inCity(e Export, city string) Export {
	out := e
	out.Streets = slices.DeleteFunc(slices.Clone(e.Streets), func(s ExportStreet) bool { return s.City != city })

	return out
}

func TestTransferExport_FiltersBySource(t *testing.T) {
	t.Parallel()

	f := newOwnershipFixture(t)
	all := slices.Concat(summary(manualStreet, "1", "on manual"), summary(ownedStreet, "1", ownedGeoName))

	tests := []struct {
		name   string
		filter SourceFilter
		want   []string
	}{
		{"everything", SourceFilter{}, all},
		{"manual rows", SourceFilter{Manual: true}, summary(manualStreet, "1")},
		// The collector's geo object on the manual street brings that street
		// and its house number along.
		{"collector", SourceFilter{Collectors: []string{f.id}}, all},
		{"other collector", SourceFilter{Collectors: []string{"none-" + f.id}}, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			doc, err := NewTransferStore(f.db).Export(t.Context(), tt.filter)
			if err != nil {
				t.Fatal(err)
			}

			if got := summarize(inCity(doc, f.city)); !slices.Equal(got, tt.want) {
				t.Errorf("Export = %v, want %v", got, tt.want)
			}

			if err := doc.Validate(); err != nil {
				t.Errorf("exported document is invalid: %v", err)
			}
		})
	}
}

// exportCopy exports f's rows (all of them, as the collector's geo object
// is on the manual street) and moves them to a city of their own, with
// geo objects of their own, so importing them creates new rows. It removes
// those rows when t ends.
func exportCopy(t *testing.T, f ownershipFixture) Export {
	t.Helper()

	doc, err := NewTransferStore(f.db).Export(t.Context(), SourceFilter{Collectors: []string{f.id}})
	if err != nil {
		t.Fatal(err)
	}

	doc = inCity(doc, f.city)
	city := "Copy " + f.city

	for i := range doc.Streets {
		s := &doc.Streets[i]
		s.City = city

		for j := range s.GeoObjects {
			s.GeoObjects[j].Group = "copy " + s.GeoObjects[j].Group
			s.GeoObjects[j].ExternalID = nil
		}
	}

	t.Cleanup(func() { f.db.Delete(&Street{}, "city = ?", city) })

	return doc
}

func wantImportStats(t *testing.T, what string, got, want ImportEntityStats) {
	t.Helper()

	if got != want {
		t.Errorf("%s = %+v, want %+v", what, got, want)
	}
}

func TestTransferImport_CreatesThenMatches(t *testing.T) {
	t.Parallel()

	f := newOwnershipFixture(t)
	ts := NewTransferStore(f.db)
	doc := exportCopy(t, f)
	city := doc.Streets[0].City

	dry, err := ts.Import(t.Context(), doc, ImportOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	wantImportStats(t, "dry run streets", dry.Streets, ImportEntityStats{Created: 2})
	wantImportStats(t, "dry run house numbers", dry.HouseNumbers, ImportEntityStats{Created: 2})
	wantImportStats(t, "dry run geo objects", dry.GeoObjects, ImportEntityStats{Created: 2})

	var count int64
	if err := f.db.Model(&Street{}).Where("city = ?", city).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("a dry run left %d streets (err %v)", count, err)
	}

	created, err := ts.Import(t.Context(), doc, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if dry.DryRun = false; created != dry {
		t.Errorf("import = %+v, want what the dry run said: %+v", created, dry)
	}

	// Imported rows are manual, whoever owned them before.
	var owned int64
	if err := f.db.Model(&Street{}).Where("city = ? AND collector_id IS NOT NULL", city).Count(&owned).Error; err != nil || owned != 0 {
		t.Errorf("%d imported streets have a collector (err %v)", owned, err)
	}

	again, err := ts.Import(t.Context(), doc, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}

	wantImportStats(t, "streets again", again.Streets, ImportEntityStats{Unchanged: 2})
	wantImportStats(t, "house numbers again", again.HouseNumbers, ImportEntityStats{Unchanged: 2})
	wantImportStats(t, "geo objects again", again.GeoObjects, ImportEntityStats{Unchanged: 2})

	doc.Streets[0].HouseNumbers[0].Postcode = "99999"
	doc.Streets[0].GeoObjects[0].Longitude++

	skipped, err := ts.Import(t.Context(), doc, ImportOptions{Mode: ImportSkip})
	if err != nil {
		t.Fatal(err)
	}

	wantImportStats(t, "skipped house numbers", skipped.HouseNumbers, ImportEntityStats{Unchanged: 1, Skipped: 1})
	wantImportStats(t, "skipped geo objects", skipped.GeoObjects, ImportEntityStats{Unchanged: 1, Skipped: 1})

	updated, err := ts.Import(t.Context(), doc, ImportOptions{Mode: ImportUpdate})
	if err != nil {
		t.Fatal(err)
	}

	wantImportStats(t, "updated house numbers", updated.HouseNumbers, ImportEntityStats{Unchanged: 1, Updated: 1})
	wantImportStats(t, "updated geo objects", updated.GeoObjects, ImportEntityStats{Unchanged: 1, Updated: 1})

	var h HouseNumber

	err = f.db.Joins("JOIN streets s ON s.id = house_numbers.street_id").
		Where("s.city = ? AND s.name = ?", city, doc.Streets[0].Name).Take(&h).Error
	if err != nil || h.Postcode != "99999" {
		t.Errorf("updated house number = %+v (err %v), want postcode 99999", h, err)
	}
}

func TestTransferImport_LeavesCollectorRowsAlone(t *testing.T) {
	t.Parallel()

	f := newOwnershipFixture(t)
	ts := NewTransferStore(f.db)

	doc, err := ts.Export(t.Context(), SourceFilter{Collectors: []string{f.id}})
	if err != nil {
		t.Fatal(err)
	}

	doc = inCity(doc, f.city)
	for i := range doc.Streets {
		doc.Streets[i].Latitude++
	}

	result, err := ts.Import(t.Context(), doc, ImportOptions{Mode: ImportUpdate, Filter: SourceFilter{Collectors: []string{f.id}}})
	if err != nil {
		t.Fatal(err)
	}

	// The manual street comes along for the collector's geo object on it,
	// and so is updated; the collector's street is not.
	wantImportStats(t, "streets", result.Streets, ImportEntityStats{Updated: 1, Skipped: 1})
	wantImportStats(t, "geo objects", result.GeoObjects, ImportEntityStats{Unchanged: 2})

	if got := f.street(t, ownedStreet); got.Latitude != f.owned.Latitude {
		t.Errorf("collector's street moved to %v, want %v", got.Latitude, f.owned.Latitude)
	}
}
