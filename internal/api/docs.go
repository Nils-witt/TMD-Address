package api

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed openapi.yaml
var openapiSpec []byte

const docsPageHTML = `<!doctype html>
<html>
<head>
  <title>address-serv API docs</title>
  <meta charset="utf-8" />
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      SwaggerUIBundle({
        url: "/docs/openapi.yaml",
        dom_id: "#swagger-ui",
      });
    };
  </script>
</body>
</html>`

// registerDocsRoutes serves Swagger UI and the OpenAPI spec outside the auth
// middleware, so the docs are publicly readable.
func registerDocsRoutes(rg gin.IRouter) {
	get(rg, "/docs", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(docsPageHTML))
	})

	get(rg, "/docs/openapi.yaml", func(c *gin.Context) {
		c.Data(http.StatusOK, "application/yaml", openapiSpec)
	})
}
