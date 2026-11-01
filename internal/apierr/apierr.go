// Package apierr defines the structured errors Hangar returns on every
// interface (HTTP, CLI, MCP). They are written for an LLM reader: a stable
// machine-readable code, a plain-language message, and where possible a
// concrete suggested fix or next step.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

type Error struct {
	Status       int            `json:"-"`
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Field        string         `json:"field,omitempty"`
	SuggestedFix any            `json:"suggested_fix,omitempty"`
	Hint         string         `json:"hint,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func New(status int, code, format string, args ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) WithField(f string) *Error { e.Field = f; return e }
func (e *Error) WithFix(fix any) *Error    { e.SuggestedFix = fix; return e }
func (e *Error) WithHint(h string) *Error  { e.Hint = h; return e }
func (e *Error) WithDetail(k string, v any) *Error {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[k] = v
	return e
}

// As returns err as an *Error if it is (or wraps) one.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// From converts any error into an *Error, hiding unknown internals.
func From(err error) *Error {
	if e, ok := As(err); ok {
		if e.Status == 0 {
			e.Status = http.StatusInternalServerError
		}
		return e
	}
	return New(http.StatusInternalServerError, "internal", "internal error")
}

func Invalid(field, format string, args ...any) *Error {
	return New(http.StatusBadRequest, "invalid_spec", format, args...).WithField(field)
}

func NotFound(kind, id string) *Error {
	return New(http.StatusNotFound, kind+"_not_found", "%s %q not found", kind, id)
}
