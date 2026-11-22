package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nils-witt/tmd-address/internal/store"
	"github.com/nils-witt/tmd-address/internal/store/storetest"
)

// TestTransferHandlers_RejectBadRequests checks the 400 responses served
// before the store is reached, so no database is needed.
func TestTransferHandlers_RejectBadRequests(t *testing.T) {
	t.Parallel()

	const validDoc = `{"formatVersion":1,"streets":[]}`

	tests := []struct {
		name    string
		handler http.HandlerFunc
		method  string
		target  string
		body    string
	}{
		{"export with invalid manual", handleExport(nil), http.MethodGet, "/api/export?manual=maybe", ""},
		{"export with empty collector", handleExport(nil), http.MethodGet, "/api/export?collector=", ""},
		{"import with invalid mode", handleImport(nil), http.MethodPost, "/api/import?mode=replace", validDoc},
		{"import with invalid dry run", handleImport(nil), http.MethodPost, "/api/import?dryRun=maybe", validDoc},
		{"import with empty collector", handleImport(nil), http.MethodPost, "/api/import?collector=", validDoc},
		{"import with invalid json", handleImport(nil), http.MethodPost, "/api/import", "{"},
		{"import of another format version", handleImport(nil), http.MethodPost, "/api/import", `{"formatVersion":2,"streets":[]}`},
		{"import of an invalid street", handleImport(nil), http.MethodPost, "/api/import", `{"formatVersion":1,"streets":[{"name":"A"}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), tt.method, tt.target, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()

			tt.handler(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (body %q)", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
		})
	}
}

// TestTransfer_ExportImportsIntoAnotherDatabase exports the manual rows of
// one database and imports them into another through the handlers.
func TestTransfer_ExportImportsIntoAnotherDatabase(t *testing.T) {
	t.Parallel()

	from, to := storetest.OpenIsolated(t), storetest.OpenIsolated(t)
	ctx := t.Context()

	s, err := store.NewStreetStore(from).Create(ctx, store.Street{Name: "A", District: "D", City: "C", Country: "XX", Latitude: 50, Longitude: 7})
	if err != nil {
		t.Fatal(err)
	}

	h, err := store.NewHouseNumberStore(from).Create(ctx, store.HouseNumber{StreetID: s.ID, Number: 1, Postcode: "11111", Latitude: 50, Longitude: 7})
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.NewGeoObjectStore(from).Create(ctx, store.GeoObject{
		Name: "h1", Group: "hydrant", StreetID: s.ID, HouseNumberID: &h.ID, Latitude: 50, Longitude: 7, ExternalID: new("ext-1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/export?manual=true", nil)
	rec := httptest.NewRecorder()
	handleExport(store.NewTransferStore(from))(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("export status = %d (body %q)", rec.Code, rec.Body.String())
	}

	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment; ") {
		t.Errorf("Content-Disposition = %q, want an attachment", got)
	}

	req = httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/import", rec.Body)
	rec = httptest.NewRecorder()
	handleImport(store.NewTransferStore(to))(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d (body %q)", rec.Code, rec.Body.String())
	}

	var result store.ImportResult
	if err := json.NewDecoder(rec.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}

	want := store.ImportEntityStats{Created: 1}
	if result.Streets != want || result.HouseNumbers != want || result.GeoObjects != want {
		t.Errorf("import = %+v, want one of each created", result)
	}

	g, err := store.NewGeoObjectStore(to).GetByExternalID(ctx, "ext-1")
	if err != nil {
		t.Fatal(err)
	}

	if g.HouseNumberID == nil {
		t.Error("imported geo object lost its house number")
	}
}
