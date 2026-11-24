package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/google/uuid"

	"github.com/nils-witt/tmd-address/internal/store"
)

func registerGeoObjectRoutes(rg gin.IRouter, geoObjects *store.GeoObjectStore) {
	rg.POST("/api/geo-objects", handleCreateGeoObject(geoObjects))
	get(rg, "/api/geo-objects", handleListGeoObjects(geoObjects))
	rg.POST("/api/geo-objects/bulk-delete", handleBulkDeleteGeoObjects(geoObjects))
	get(rg, "/api/geo-objects/:id", handleGetGeoObject(geoObjects))
	rg.PUT("/api/geo-objects/:id", handleUpdateGeoObject(geoObjects))
	rg.DELETE("/api/geo-objects/:id", handleDeleteGeoObject(geoObjects))
	rg.PUT("/api/geo-objects/by-external-id/:externalId", handleUpsertGeoObjectByExternalID(geoObjects))
	rg.DELETE("/api/geo-objects/by-external-id/:externalId", handleDeleteGeoObjectByExternalID(geoObjects))
}

// isGeoObjectReferenceError reports whether err means the geo object's street
// or house number reference is invalid, which is the client's fault.
func isGeoObjectReferenceError(err error) bool {
	return errors.Is(err, store.ErrStreetNotFound) ||
		errors.Is(err, store.ErrHouseNumberNotFound) ||
		errors.Is(err, store.ErrHouseNumberNotOnStreet)
}

// writeGeoObjectWriteError writes the response for an error returned by a
// geo object write.
func writeGeoObjectWriteError(c *gin.Context, err error) {
	switch {
	case isGeoObjectReferenceError(err):
		writeError(c, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrExternalIDTaken), errors.Is(err, store.ErrNameTaken), errors.Is(err, store.ErrOwnedByCollector):
		writeError(c, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(c, http.StatusNotFound, "geo object not found")
	default:
		writeError(c, http.StatusInternalServerError, err.Error())
	}
}

func handleCreateGeoObject(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var g store.GeoObject
		if err := json.NewDecoder(c.Request.Body).Decode(&g); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := g.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		created, err := geoObjects.Create(c.Request.Context(), g)
		if err != nil {
			writeGeoObjectWriteError(c, err)
			return
		}

		writeJSON(c, http.StatusCreated, created)
	}
}

func handleListGeoObjects(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()

		filter := store.GeoObjectFilter{
			Name:             query.Get("name"),
			Group:            query.Get("group"),
			ExternalID:       query.Get("externalId"),
			ExternalIDPrefix: query.Get("externalIdPrefix"),
		}

		var err error

		if filter.StreetID, err = optionalUUIDParam(query, "streetId"); err != nil {
			writeError(c, http.StatusBadRequest, "invalid streetId")
			return
		}

		if filter.HouseNumberID, err = optionalUUIDParam(query, "houseNumberId"); err != nil {
			writeError(c, http.StatusBadRequest, "invalid houseNumberId")
			return
		}

		list, err := geoObjects.List(c.Request.Context(), filter)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, list)
	}
}

//nolint:dupl // mirrors the get/update/delete house-number handlers; distinct entity types make a shared generic handler less readable than the duplication
func handleGetGeoObject(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		g, err := geoObjects.Get(c.Request.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "geo object not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, g)
	}
}

func handleUpdateGeoObject(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		var g store.GeoObject
		if err := json.NewDecoder(c.Request.Body).Decode(&g); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := g.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		updated, err := geoObjects.Update(c.Request.Context(), id, g)
		if err != nil {
			writeGeoObjectWriteError(c, err)
			return
		}

		writeJSON(c, http.StatusOK, updated)
	}
}

func handleDeleteGeoObject(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		if err := geoObjects.Delete(c.Request.Context(), id); err != nil {
			writeGeoObjectWriteError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	}
}

// bulkDeleteResult is the response of a bulk delete.
type bulkDeleteResult struct {
	Deleted int64 `json:"deleted"`
}

func handleBulkDeleteGeoObjects(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		var refs store.GeoObjectRefs
		if err := json.NewDecoder(c.Request.Body).Decode(&refs); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := refs.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		deleted, err := geoObjects.DeleteMany(c.Request.Context(), refs)
		if errors.Is(err, store.ErrOwnedByCollector) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, bulkDeleteResult{Deleted: deleted})
	}
}

// externalIDFromRequest returns the externalId path value, or an error if it
// is empty or too long.
func externalIDFromRequest(c *gin.Context) (string, error) {
	externalID := c.Param("externalId")
	if err := (store.GeoObject{ExternalID: &externalID}).ValidateExternalID(); err != nil {
		return "", err
	}

	return externalID, nil
}

func handleUpsertGeoObjectByExternalID(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		externalID, err := externalIDFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		var g store.GeoObject
		if err := json.NewDecoder(c.Request.Body).Decode(&g); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		g.ExternalID = &externalID

		if err := g.Validate(); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		saved, created, err := geoObjects.UpsertByExternalID(c.Request.Context(), externalID, g)
		if err != nil {
			writeGeoObjectWriteError(c, err)
			return
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}

		writeJSON(c, status, saved)
	}
}

func handleDeleteGeoObjectByExternalID(geoObjects *store.GeoObjectStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		externalID, err := externalIDFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		if err := geoObjects.DeleteByExternalID(c.Request.Context(), externalID); err != nil {
			writeGeoObjectWriteError(c, err)
			return
		}

		c.Status(http.StatusNoContent)
	}
}

// optionalUUIDParam parses the query parameter key as a UUID, returning nil
// if it is absent or empty.
func optionalUUIDParam(query url.Values, key string) (*uuid.UUID, error) {
	v := query.Get(key)
	if v == "" {
		return nil, nil //nolint:nilnil // an absent parameter is not an error and has no value
	}

	id, err := uuid.Parse(v)
	if err != nil {
		return nil, err
	}

	return &id, nil
}
