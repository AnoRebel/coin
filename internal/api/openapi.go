package api

import (
	_ "embed"
	"net/http"

	"github.com/labstack/echo/v5"
)

//go:embed openapi.json
var openAPIDocument []byte

func (s *Server) openapi(c *echo.Context) error {
	return c.Blob(http.StatusOK, "application/json", openAPIDocument)
}

// deprecatedHandler adds the standard deprecation signals to a legacy route.
func deprecatedHandler(next echo.HandlerFunc, sunset, successor string) echo.HandlerFunc {
	return func(c *echo.Context) error {
		header := c.Response().Header()
		header.Set("Deprecation", "true")
		header.Set("Sunset", sunset)
		header.Set("Link", "<"+successor+">; rel=\"successor-version\"")
		return next(c)
	}
}
