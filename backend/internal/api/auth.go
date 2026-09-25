package api

import (
	"errors"
	"net/http"
	"strings"
)

// extractBearerToken checks the header format only. OpenEverest will verify
// the token and target access when credential lookup is implemented.
func extractBearerToken(r *http.Request) (string, error) {
	values := r.Header.Values("Authorization")
	if len(values) == 1 {
		parts := strings.Fields(values[0])
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return parts[1], nil
		}
	}
	return "", errors.New("a single Bearer authorization token is required")
}
