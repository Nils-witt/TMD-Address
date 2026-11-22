package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/nils-witt/tmd-address/internal/store"
)

func TestHandleSuggestReverseValidation(t *testing.T) {
	t.Parallel()

	handler := handleSuggestReverse(nil, nil)

	tests := []struct{ name, query string }{
		{"missing lat and lon", ""},
		{"lat out of range", "lat=-90.1&lon=7"},
		{"lon NaN", "lat=50&lon=NaN"},
		{"limit zero", "lat=50&lon=7&limit=0"},
		{"limit above 20", "lat=50&lon=7&limit=21"},
		{"limit not an integer", "lat=50&lon=7&limit=x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/suggest/reverse?"+tt.query, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusBadRequest, rec.Body)
			}
		})
	}
}

func TestSuggestReverse(t *testing.T) {
	t.Parallel()

	a := "b"
	street := store.Street{Name: "Hauptstraße", District: "Mitte", City: "Bonn", Country: "Germany"}
	number := store.HouseNumber{Number: 12, NumberAddition: &a, Postcode: "53111", Latitude: 50.1, Longitude: 7.1}

	got := suggestReverse(
		[]store.NearbyHouseNumber{{HouseNumber: number, Street: street, DistanceMeters: 4}},
		[]store.NearbyGeoObject{
			{GeoObject: store.GeoObject{Name: "H1", Group: "Hydrants", Latitude: 50.2, Longitude: 7.2}, Street: street, HouseNumber: &number, DistanceMeters: 9},
			{GeoObject: store.GeoObject{Name: "H2", Group: "Hydrants"}, Street: street, DistanceMeters: 12},
		},
	)

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}

	const want = `{"addresses":[{"street":"Hauptstraße","house_number":"12b","postcode":"53111","district":"Mitte","city":"Bonn",` +
		`"country":"Germany","latitude":50.1,"longitude":7.1,"distance_meters":4}],` +
		`"geo_objects":[{"name":"H1","group":"Hydrants","street":"Hauptstraße","house_number":"12b","postcode":"53111",` +
		`"district":"Mitte","city":"Bonn","country":"Germany","latitude":50.2,"longitude":7.2,"distance_meters":9},` +
		`{"name":"H2","group":"Hydrants","street":"Hauptstraße","house_number":"","postcode":"","district":"Mitte",` +
		`"city":"Bonn","country":"Germany","latitude":0,"longitude":0,"distance_meters":12}]}`

	if string(body) != want {
		t.Fatalf("got  %s\nwant %s", body, want)
	}
}

// TestSuggestRoutes checks the routes against a real database: each answers
// a JSON array, empty rather than null, and reads its query parameters.
func TestSuggestRoutes(t *testing.T) {
	t.Parallel()

	dsn := os.Getenv("ADDRESS_SERV_TEST_DSN")
	if dsn == "" {
		t.Skip("ADDRESS_SERV_TEST_DSN not set")
	}

	db, err := store.Open(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = store.Close(db) })

	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	tag := uuid.NewString()
	city := "Routecity " + tag

	results, err := store.NewBulkStore(db).Create(t.Context(), []store.BulkStreetInput{{
		City: city, District: "Mitte", Name: "Routeweg " + tag, Country: "Germany", Latitude: 50, Longitude: 7,
		HouseNumbers: []store.BulkHouseNumberInput{{Number: 3, Postcode: "53111", Latitude: 50, Longitude: 7}},
	}})
	if err != nil {
		t.Fatal(err)
	}

	streets := store.NewStreetStore(db)

	t.Cleanup(func() { _ = streets.Delete(t.Context(), results[0].Street.ID) })

	mux := http.NewServeMux()
	registerSuggestRoutes(mux, store.NewSuggestStore(db), store.NewHouseNumberStore(db), store.NewGeoObjectStore(db))

	q := func(kv ...string) string {
		v := url.Values{}
		for i := 0; i+1 < len(kv); i += 2 {
			v.Set(kv[i], kv[i+1])
		}

		return "?" + v.Encode()
	}

	tests := []struct {
		name, path string
		wantLen    int
	}{
		{"streets", "/api/suggest/streets" + q("name", "routeweg "+tag, "city", city), 1},
		{"streets too short", "/api/suggest/streets" + q("name", "R"), 0},
		{"house numbers", "/api/suggest/house-numbers" + q("street", "Routeweg "+tag, "city", city), 1},
		{"cities", "/api/suggest/cities" + q("name", tag), 1},
		{"districts", "/api/suggest/districts" + q("city", city), 1},
		{"geo objects", "/api/suggest/geo-objects" + q("name", tag), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body)
			}

			var list []json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list == nil {
				t.Fatalf("body %s is not a JSON array", rec.Body)
			}

			if len(list) != tt.wantLen {
				t.Fatalf("got %d items, want %d: %s", len(list), tt.wantLen, rec.Body)
			}
		})
	}
}
