package api

import (
	"avxxx/internal/store"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// httpError is an error that carries the HTTP status to reply with and a
// message that is safe to show to clients.
type httpError struct {
	status int
	msg    string
	err    error // optional cause, logged but never sent to the client
}

func (e *httpError) Error() string {
	if e.err != nil {
		return e.msg + ": " + e.err.Error()
	}
	return e.msg
}

func (e *httpError) Unwrap() error { return e.err }

// Is lets errors.Is(err, errUnauthorized) etc. match on status alone.
func (e *httpError) Is(target error) bool {
	t, ok := target.(*httpError)
	return ok && t.status == e.status
}

// Sentinels for errors.Is matching; use the constructors to attach a message or cause.
var (
	errBadRequest   = &httpError{status: http.StatusBadRequest, msg: statusMessage(http.StatusBadRequest)}
	errUnauthorized = &httpError{status: http.StatusUnauthorized, msg: statusMessage(http.StatusUnauthorized)}
	errNotFound     = &httpError{status: http.StatusNotFound, msg: statusMessage(http.StatusNotFound)}
	errConflict     = &httpError{status: http.StatusConflict, msg: statusMessage(http.StatusConflict)}
)

func statusMessage(status int) string { return strings.ToLower(http.StatusText(status)) }

// badRequest returns a 400 error with a client-visible message and optional cause.
func badRequest(msg string, cause error) error {
	return &httpError{status: http.StatusBadRequest, msg: msg, err: cause}
}

// unauthorized returns a 401 error wrapping an optional cause.
func unauthorized(cause error) error {
	return &httpError{status: http.StatusUnauthorized, msg: statusMessage(http.StatusUnauthorized), err: cause}
}

// errorStatus maps e to an HTTP status and the message sent to the client.
// Unknown errors become 500 with a generic message so internal text never leaks.
func errorStatus(e error) (int, string) {
	var he *httpError
	var ce *store.ConflictError
	var ve *store.ValidationError
	var ae *store.AgeGateError
	switch {
	case errors.As(e, &he):
		return he.status, he.msg
	case errors.As(e, &ce):
		return http.StatusConflict, ce.Msg
	case errors.As(e, &ve):
		return http.StatusBadRequest, ve.Msg
	case errors.As(e, &ae):
		return http.StatusConflict, ae.Error()
	case errors.Is(e, store.ErrInvalidMergeTarget):
		return http.StatusBadRequest, store.ErrInvalidMergeTarget.Error()
	case errors.Is(e, store.ErrEmailTaken):
		return http.StatusConflict, store.ErrEmailTaken.Error()
	case errors.Is(e, store.ErrInvalidCredentials):
		return http.StatusUnauthorized, store.ErrInvalidCredentials.Error()
	case errors.Is(e, store.ErrInvalidQuery):
		return http.StatusBadRequest, e.Error()
	case errors.Is(e, pgx.ErrNoRows):
		return http.StatusNotFound, statusMessage(http.StatusNotFound)
	}
	var pgErr *pgconn.PgError
	if errors.As(e, &pgErr) && pgErr.Code == "23505" { // unique_violation
		return http.StatusConflict, statusMessage(http.StatusConflict)
	}
	// Fallback for untyped store errors (store.Enqueue returns fmt.Errorf("source disabled")).
	if strings.Contains(e.Error(), "source disabled") {
		return http.StatusConflict, statusMessage(http.StatusConflict)
	}
	return http.StatusInternalServerError, "request failed"
}
