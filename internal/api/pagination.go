package api

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"

	"github.com/labstack/echo/v5"
)

// pageLimit parses limit with a default and a hard ceiling.
func pageLimit(c *echo.Context, def, max int) (int, error) {
	raw := c.QueryParam("limit")
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > max {
		return 0, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT",
			fmt.Sprintf("limit must be between 1 and %d", max))
	}
	return value, nil
}

// blockCursor parses an exclusive height cursor.
func blockCursor(c *echo.Context) (uint64, error) {
	raw := c.QueryParam("cursor")
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "cursor must be a block height")
	}
	return value, nil
}

// assetCursor parses an exclusive asset ID cursor.
func assetCursor(c *echo.Context) (string, error) {
	raw := c.QueryParam("cursor")
	if raw == "" {
		return "", nil
	}
	if !isAssetCursor(raw) {
		return "", apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "cursor must be an asset id")
	}
	return raw, nil
}

func isAssetCursor(raw string) bool {
	if len(raw) < 3 || len(raw) > 80 {
		return false
	}
	for _, r := range raw {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '/' || r == '-') {
			return false
		}
	}
	return true
}

// pageOrder returns true for descending order.
func pageOrder(c *echo.Context) (bool, error) {
	switch order := c.QueryParamOr("order", "desc"); order {
	case "desc":
		return true, nil
	case "asc":
		return false, nil
	default:
		return false, apiErr(http.StatusBadRequest, "INVALID_ARGUMENT", "order must be one of: asc, desc")
	}
}

// checkQuery rejects unknown query parameters so a typo is an error rather
// than a silently ignored filter.
func checkQuery(c *echo.Context, allowed ...string) error {
	permitted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		permitted[name] = true
	}
	var unknown []string
	for name := range c.QueryParams() {
		if !permitted[name] {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	sort.Strings(allowed)
	return apiErr(http.StatusBadRequest, "INVALID_ARGUMENT",
		fmt.Sprintf("unsupported query parameter %q; allowed: %v", unknown[0], allowed))
}
