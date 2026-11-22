package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/nils-witt/tmd-address/internal/store"
)

// maxSuggestReverseLimit caps the limit of GET /api/suggest/reverse, which
// lists places for a person to pick from.
const maxSuggestReverseLimit = 20

// registerSuggestRoutes serves /api/suggest/*: what an address form
// suggests while it is typed into, ranked and bounded for that. A browser
// calls them directly, so they answer one request per field.
func registerSuggestRoutes(mux *http.ServeMux, suggest *store.SuggestStore,
	houseNumbers *store.HouseNumberStore, geoObjects *store.GeoObjectStore,
) {
	mux.HandleFunc("GET /api/suggest/streets", suggestHandler(suggest.Streets))
	mux.HandleFunc("GET /api/suggest/house-numbers", suggestHandler(suggest.HouseNumbers))
	mux.HandleFunc("GET /api/suggest/cities", suggestHandler(suggest.Cities))
	mux.HandleFunc("GET /api/suggest/districts", suggestHandler(suggest.Districts))
	mux.HandleFunc("GET /api/suggest/geo-objects", suggestHandler(suggest.GeoObjects))
	mux.HandleFunc("GET /api/suggest/reverse", handleSuggestReverse(houseNumbers, geoObjects))
}

// suggestHandler answers the JSON array list returns for the query's name,
// street, city and district.
func suggestHandler[T any](list func(context.Context, store.SuggestQuery) ([]T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		found, err := list(r.Context(), store.SuggestQuery{
			Name:     query.Get("name"),
			Street:   query.Get("street"),
			City:     query.Get("city"),
			District: query.Get("district"),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, found)
	}
}

// suggestReverseResult is the answer of GET /api/suggest/reverse.
type suggestReverseResult struct {
	Addresses  []suggestReverseAddress   `json:"addresses"`
	GeoObjects []suggestReverseGeoObject `json:"geo_objects"`
}

// suggestReverseAddress is a house number near the query point, with its
// street.
type suggestReverseAddress struct {
	Street         string  `json:"street"`
	HouseNumber    string  `json:"house_number"`
	Postcode       string  `json:"postcode"`
	District       string  `json:"district"`
	City           string  `json:"city"`
	Country        string  `json:"country"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	DistanceMeters float64 `json:"distance_meters"`
}

// suggestReverseGeoObject is a geo object near the query point, its distance
// measured to its own position.
type suggestReverseGeoObject struct {
	store.GeoObjectSuggestion

	DistanceMeters float64 `json:"distance_meters"`
}

// handleSuggestReverse serves GET /api/suggest/reverse: like GET
// /api/reverse, the house numbers and geo objects nearest to lat/lon, each
// nearest first and at most limit (default 1), but with their addresses
// flattened as the other suggestions are.
func handleSuggestReverse(houseNumbers *store.HouseNumberStore, geoObjects *store.GeoObjectStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()

		lat, err := coordinateParam(query, "lat", 90)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		lon, err := coordinateParam(query, "lon", 180)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		limit := 1
		if v := query.Get("limit"); v != "" {
			limit, err = strconv.Atoi(v)
			if err != nil || limit < 1 || limit > maxSuggestReverseLimit {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("limit must be an integer between 1 and %d", maxSuggestReverseLimit))
				return
			}
		}

		numbers, err := houseNumbers.Nearest(r.Context(), lat, lon, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		objects, err := geoObjects.Nearest(r.Context(), lat, lon, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(w, http.StatusOK, suggestReverse(numbers, objects))
	}
}

// suggestReverse flattens the nearest house numbers and geo objects.
func suggestReverse(numbers []store.NearbyHouseNumber, objects []store.NearbyGeoObject) suggestReverseResult {
	out := suggestReverseResult{
		Addresses:  make([]suggestReverseAddress, len(numbers)),
		GeoObjects: make([]suggestReverseGeoObject, len(objects)),
	}

	for i, n := range numbers {
		out.Addresses[i] = suggestReverseAddress{
			Street:      n.Street.Name,
			HouseNumber: store.HouseNumberText(n.HouseNumber.Number, n.HouseNumber.NumberAddition),
			Postcode:    n.HouseNumber.Postcode,
			District:    n.Street.District, City: n.Street.City, Country: n.Street.Country,
			Latitude: n.HouseNumber.Latitude, Longitude: n.HouseNumber.Longitude,
			DistanceMeters: n.DistanceMeters,
		}
	}

	for i, g := range objects {
		o := store.GeoObjectSuggestion{
			Name: g.GeoObject.Name, Group: g.GeoObject.Group,
			Street: g.Street.Name, District: g.Street.District, City: g.Street.City, Country: g.Street.Country,
			Latitude: g.GeoObject.Latitude, Longitude: g.GeoObject.Longitude,
		}
		if g.HouseNumber != nil {
			o.HouseNumber = store.HouseNumberText(g.HouseNumber.Number, g.HouseNumber.NumberAddition)
			o.Postcode = g.HouseNumber.Postcode
		}

		out.GeoObjects[i] = suggestReverseGeoObject{GeoObjectSuggestion: o, DistanceMeters: g.DistanceMeters}
	}

	return out
}
