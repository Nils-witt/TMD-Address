package api

import (
	"log"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// loggingMiddleware logs each request with its status and duration, in the
// format the service logged before it moved to Gin.
func loggingMiddleware(c *gin.Context) {
	start := time.Now()

	c.Next()

	method := strconv.Quote(c.Request.Method)
	path := strconv.Quote(c.Request.URL.Path)
	log.Printf("%s %s %d %s", method, path, c.Writer.Status(), time.Since(start))
}
