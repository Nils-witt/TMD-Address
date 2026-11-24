package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/nils-witt/tmd-address/internal/store"
)

const maxAPITokenNameLen = 100

func registerAPITokenRoutes(rg gin.IRouter, tokens *store.APITokenStore) {
	get(rg, "/api/tokens", handleListAPITokens(tokens))
	rg.POST("/api/tokens", handleCreateAPIToken(tokens))
	rg.DELETE("/api/tokens/:id", handleDeleteAPIToken(tokens))
}

// requireTokenOwner returns the principal allowed to manage API tokens, or
// writes a 403 and returns false. Tokens are managed only from an OIDC
// session, so a leaked API token can't be used to mint more of them.
func requireTokenOwner(c *gin.Context) (principal, bool) {
	p, ok := principalFrom(c.Request.Context())
	if !ok || p.APITokenID != nil || p.Subject == "" {
		writeError(c, http.StatusForbidden, "API tokens can only be managed when signed in with OpenID Connect")
		return principal{}, false
	}

	return p, true
}

func handleListAPITokens(tokens *store.APITokenStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := requireTokenOwner(c)
		if !ok {
			return
		}

		list, err := tokens.List(c.Request.Context(), p.Subject)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusOK, list)
	}
}

type createAPITokenRequest struct {
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

func (req *createAPITokenRequest) validate(now time.Time) error {
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return errors.New("name is required")
	}

	if utf8.RuneCountInString(req.Name) > maxAPITokenNameLen {
		return errors.New("name must be at most 100 characters")
	}

	if req.ExpiresAt != nil && !req.ExpiresAt.After(now) {
		return errors.New("expiresAt must be in the future")
	}

	return nil
}

// createdAPIToken is the one response that carries the token secret.
type createdAPIToken struct {
	store.APIToken

	Token string `json:"token"`
}

func handleCreateAPIToken(tokens *store.APITokenStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := requireTokenOwner(c)
		if !ok {
			return
		}

		var req createAPITokenRequest
		if err := json.NewDecoder(c.Request.Body).Decode(&req); err != nil {
			writeError(c, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := req.validate(time.Now()); err != nil {
			writeError(c, http.StatusBadRequest, err.Error())
			return
		}

		created, secret, err := tokens.Create(c.Request.Context(), p.Subject, p.Name, req.Name, req.ExpiresAt)
		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		writeJSON(c, http.StatusCreated, createdAPIToken{APIToken: created, Token: secret})
	}
}

func handleDeleteAPIToken(tokens *store.APITokenStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := requireTokenOwner(c)
		if !ok {
			return
		}

		id, err := idFromRequest(c)
		if err != nil {
			writeError(c, http.StatusBadRequest, "invalid id")
			return
		}

		err = tokens.Delete(c.Request.Context(), p.Subject, id)
		if errors.Is(err, store.ErrNotFound) {
			writeError(c, http.StatusNotFound, "API token not found")
			return
		}

		if err != nil {
			writeError(c, http.StatusInternalServerError, err.Error())
			return
		}

		c.Status(http.StatusNoContent)
	}
}
