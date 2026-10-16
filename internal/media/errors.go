package media

import (
	"errors"
	"fmt"
	"net/http"
)

// StatusError is a non-200 HTTP reply from a provider. Callers use
// HTTPStatus to tell rate limiting (403/429) from other failures.
type StatusError struct {
	Source string
	Code   int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Source, e.Code)
}

// HTTPStatus returns the provider HTTP status carried by err, or 0.
func HTTPStatus(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// IsRateLimited reports whether err is a provider 403 or 429: the provider
// wants us to slow down (or has blocked us), so callers stop and back off.
func IsRateLimited(err error) bool {
	c := HTTPStatus(err)
	return c == http.StatusTooManyRequests || c == http.StatusForbidden
}
