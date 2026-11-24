package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/nils-witt/tmd-address/internal/store"
)

func registerBulkRoutes(rg gin.IRouter, bulk *store.BulkStore) {
	rg.POST("/api/bulk", func(c *gin.Context) {
		var inputs []store.BulkStreetInput
		if err := json.NewDecoder(c.Request.Body).Decode(&inputs); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if len(inputs) == 0 {
			writeError(c, http.StatusBadRequest, "at least one street is required")
			return
		}

		for i, input := range inputs {
			if err := input.Validate(); err != nil {
				writeError(c, http.StatusBadRequest, fmt.Sprintf("streets[%d]: %s", i, err.Error()))
				return
			}
		}

		results, err := bulk.Create(c.Request.Context(), inputs)
		if errors.Is(err, store.ErrStreetAlreadyExists) || errors.Is(err, store.ErrHouseNumberAlreadyExists) {
			writeError(c, http.StatusConflict, err.Error())
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusCreated, results)
	})
}
