package domain

import (
	"errors"
	"strings"
)

// Expected errors. The API layers map them to their protocol's codes; anything else is an internal
// error.
var (
	ErrNotFound        = errors.New("not found")
	ErrInvalid         = errors.New("invalid request")
	ErrConflict        = errors.New("conflict")
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("access denied")
	ErrPrecondition    = errors.New("failed precondition")
	ErrTooManyAttempts = errors.New("too many attempts")
	ErrBusy            = errors.New("server busy")
)

// Error is an expected error meant for the user. It carries no sentence, only a stable code and
// params that the client translates. The server writes the text itself only as a fallback
// (internal/i18n).
type Error struct {
	Kind error
	// Code looks like "library.not_found" or "auth.username_too_long": a domain, a dot, a reason in
	// snake_case. Once published a code never changes meaning.
	Code string
	// Params are the named parameters of the message (see Text).
	Params map[string]string
	// Causes explain an error that has several reasons (unplayable file, rejected theme).
	Causes []Text
}

// Error prints the code and its params. Logs and tests read this.
func (e *Error) Error() string { return e.Text().String() }

// Unwrap makes errors.Is(err, domain.ErrNotFound) work.
func (e *Error) Unwrap() error { return e.Kind }

// Text is the error as a text. Its catalog key is "error." followed by the code.
func (e *Error) Text() Text {
	return Text{Key: "error." + e.Code, Params: e.Params, List: e.Causes}
}

// CodeOf returns the code of a domain error, or an empty string.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func newError(kind error, code string, kv []any) error {
	t := T(code, kv...)
	return &Error{Kind: kind, Code: code, Params: t.Params, Causes: t.List}
}

// FromText builds an error from a text whose key is "error." followed by the code.
func FromText(kind error, t Text) error {
	return &Error{Kind: kind, Code: strings.TrimPrefix(t.Key, "error."), Params: t.Params, Causes: t.List}
}

// NotFound reports a missing resource. Arguments work as in T: the code, then name/value pairs; a
// []Text value gives the causes.
func NotFound(code string, kv ...any) error { return newError(ErrNotFound, code, kv) }

// Invalid reports a malformed request or a rejected value.
func Invalid(code string, kv ...any) error { return newError(ErrInvalid, code, kv) }

// Conflict reports a resource that already exists.
func Conflict(code string, kv ...any) error { return newError(ErrConflict, code, kv) }

// Unauthenticated reports missing or bad credentials.
func Unauthenticated(code string, kv ...any) error { return newError(ErrUnauthenticated, code, kv) }

// Forbidden reports something the caller is not allowed to do.
func Forbidden(code string, kv ...any) error { return newError(ErrForbidden, code, kv) }

// Precondition reports something that cannot be done in the current state.
func Precondition(code string, kv ...any) error { return newError(ErrPrecondition, code, kv) }

// TooManyAttempts tells the caller to slow down.
func TooManyAttempts(code string, kv ...any) error { return newError(ErrTooManyAttempts, code, kv) }

// Busy reports a server resource that is fully in use (concurrent transcodes). The request is fine
// and will go through later.
func Busy(code string, kv ...any) error { return newError(ErrBusy, code, kv) }
