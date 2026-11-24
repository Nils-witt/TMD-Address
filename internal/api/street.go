package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/nils-witt/tmd-address/internal/store"
)

func registerStreetRoutes(rg gin.IRouter, streets *store.StreetStore) {
	rg.POST("/api/streets", handleCreateStreet(streets))
	get(rg, "/api/streets", handleListStreets(streets))
	get(rg, "/api/cities", handleListCities(streets))
	get(rg, "/api/districts", handleListDistricts(streets))
	get(rg, "/api/streets/:id", handleGetStreet(streets))
	rg.PUT("/api/streets/:id", handleUpdateStreet(streets))
	rg.DELETE("/api/streets/:id", handleDeleteStreet(streets))
}

func handleCreateStreet(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var s store.Street
		if err := json.NewDecoder(c.Request.Body).Decode(&s); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := s.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		created, err := streets.Create(c.Request.Context(), s)
		if errors.Is(err, store.ErrStreetAlreadyExists) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusCreated, created)
	}
}

func handleListStreets(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()
		filter := store.StreetFilter{
			City:     query.Get("city"),
			District: query.Get("district"),
			Name:     query.Get("name"),
			Country:  query.Get("country"),
		}

		list, err := streets.List(c.Request.Context(), filter)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, list)
	}
}

func handleListCities(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		cities, err := streets.ListCities(c.Request.Context(), c.Query("name"))
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, cities)
	}
}

func handleListDistricts(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()
		filter := store.DistrictFilter{
			Name: query.Get("name"),
			City: query.Get("city"),
		}

		districts, err := streets.ListDistricts(c.Request.Context(), filter)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, districts)
	}
}

//nolint:dupl // mirrors the get/update/delete house-number handlers; distinct entity types make a shared generic handler less readable than the duplication
func handleGetStreet(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		s, err := streets.Get(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "street not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, s)
	}
}

func handleUpdateStreet(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		var s store.Street
		if err := json.NewDecoder(c.Request.Body).Decode(&s); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := s.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		updated, err := streets.Update(c.Request.Context(), id, s)
		if errors.Is(err, store.ErrStreetAlreadyExists) || errors.Is(err, store.ErrOwnedByCollector) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "street not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, updated)
	}
}

//nolint:dupl // mirrors handleDeleteHouseNumber and handleDeleteGeoObject; distinct entity types make a shared generic handler less readable than the duplication
func handleDeleteStreet(streets *store.StreetStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		err = streets.Delete(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "street not found")
			return
		}

		if errors.Is(err, store.ErrOwnedByCollector) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		c.Status(http.StatusNoContent)
	}
}
