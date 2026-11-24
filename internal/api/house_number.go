package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"github.com/nils-witt/tmd-address/internal/store"
)

func registerHouseNumberRoutes(rg gin.IRouter, houseNumbers *store.HouseNumberStore) {
	rg.POST("/api/house-numbers", handleCreateHouseNumber(houseNumbers))
	get(rg, "/api/house-numbers", handleListHouseNumbers(houseNumbers))
	get(rg, "/api/house-numbers/:id", handleGetHouseNumber(houseNumbers))
	rg.PUT("/api/house-numbers/:id", handleUpdateHouseNumber(houseNumbers))
	rg.DELETE("/api/house-numbers/:id", handleDeleteHouseNumber(houseNumbers))
}

func handleCreateHouseNumber(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var h store.HouseNumber
		if err := json.NewDecoder(c.Request.Body).Decode(&h); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := h.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		created, err := houseNumbers.Create(c.Request.Context(), h)
		if errors.Is(err, store.ErrStreetNotFound) {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		if errors.Is(err, store.ErrHouseNumberAlreadyExists) {
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

func handleListHouseNumbers(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()

		filter := store.HouseNumberFilter{
			Postcode:   query.Get("postcode"),
			StreetName: query.Get("street"),
			City:       query.Get("city"),
			District:   query.Get("district"),
			Country:    query.Get("country"),
		}

		if v := query.Get("streetId"); v != "" {
			id, err := uuid.Parse(v)
			if err != nil {
				writeError(c, http.StatusBadRequest, "invalid streetId")
				return
			}

			filter.StreetID = &id
		}

		if v := query.Get("number"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				writeError(c, http.StatusBadRequest, "invalid number")
				return
			}

			filter.Number = &n
		}

		list, err := houseNumbers.List(c.Request.Context(), filter)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, list)
	}
}

//nolint:dupl // mirrors the get/update/delete street handlers; distinct entity types make a shared generic handler less readable than the duplication
func handleGetHouseNumber(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		h, err := houseNumbers.Get(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "house number not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, h)
	}
}

func handleUpdateHouseNumber(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		var h store.HouseNumber
		if err := json.NewDecoder(c.Request.Body).Decode(&h); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := h.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		updated, err := houseNumbers.Update(c.Request.Context(), id, h)
		if errors.Is(err, store.ErrStreetNotFound) {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		if errors.Is(err, store.ErrHouseNumberAlreadyExists) || errors.Is(err, store.ErrOwnedByCollector) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "house number not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, updated)
	}
}

//nolint:dupl // mirrors handleDeleteStreet and handleDeleteGeoObject; distinct entity types make a shared generic handler less readable than the duplication
func handleDeleteHouseNumber(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		err = houseNumbers.Delete(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "house number not found")
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
