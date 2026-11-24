package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/nils-witt/tmd-address/internal/store"
)

const (
	defaultAddressSearchLimit = 10
	maxAddressSearchLimit     = 50
	minAddressSearchLength    = 2
)

func registerAddressSearchRoutes(rg gin.IRouter, houseNumbers *store.HouseNumberStore) {
	get(rg, "/api/addresses/search", handleSearchAddresses(houseNumbers))
}

// parseAddressSearchLimit parses the limit query parameter, defaulting to
// defaultAddressSearchLimit when it is empty.
func parseAddressSearchLimit(v string) (int, bool) {
	if v == "" {
		return defaultAddressSearchLimit, true
	}

	limit, err := strconv.Atoi(v)
	if err != nil || limit < 1 || limit > maxAddressSearchLimit {
		return 0, false
	}

	return limit, true
}

func handleSearchAddresses(houseNumbers *store.HouseNumberStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Request.URL.Query()

		q := store.ParseAddressQuery(query.Get("q"))
		if len([]rune(strings.TrimSpace(q.Street))) < minAddressSearchLength {
			writeError(c, http.StatusBadRequest, "q must name at least 2 characters of a street")
			return
		}

		limit, ok := parseAddressSearchLimit(query.Get("limit"))
		if !ok {
			writeError(c, http.StatusBadRequest, "limit must be between 1 and 50")
			return
		}

		matches, err := houseNumbers.SearchAddresses(c.Request.Context(), q, limit)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, matches)
	}
}
