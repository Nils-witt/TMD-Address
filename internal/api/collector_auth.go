package api

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// groupsClaim is the OIDC claim listing the groups a user is in, as Authentik
// and Keycloak's group mapper send it.
const groupsClaim = "groups"

// requireAdminGroup lets only members of group past it. With
// no group configured nobody does, and API tokens never do: they carry no
// claims.
func requireAdminGroup(group string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, _ := principalFrom(c.Request.Context())

		switch {
		case group == "":
			writeError(c, http.StatusForbidden, "managing collectors is disabled: set COLLECTOR_ADMIN_GROUP")
		case p.APITokenID != nil:
			writeError(c, http.StatusForbidden, "API tokens cannot manage collectors")
		case !inGroup(p.Claims, group):
			writeError(c, http.StatusForbidden, fmt.Sprintf("managing collectors requires membership in the group %q", group))
		}
	}
}

// inGroup reports whether the groups claim lists group. A single group sent
// as a plain string counts too.
func inGroup(claims map[string]any, group string) bool {
	switch groups := claims[groupsClaim].(type) {
	case string:
		return groups == group
	case []any:
		for _, g := range groups {
			if s, ok := g.(string); ok && s == group {
				return true
			}
		}
	}

	return false
}
