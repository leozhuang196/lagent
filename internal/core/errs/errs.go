// Package errs defines the error codes shared by every layer.
//
// Transport maps codes to HTTP status; everything else only checks codes with
// Is, never string matching.
package errs

import (
	"errors"
	"fmt"
	"strings"
)

// Code is a stable, machine-readable error category.
type Code string

const (
	CodeNotFound        Code = "not_found"
	CodeInvalid         Code = "invalid"
	CodeConflict        Code = "conflict"         // state does not allow the operation
	CodeVersionConflict Code = "version_conflict" // optimistic concurrency lost; retry with fresh state
	CodeAlreadyResolved Code = "already_resolved" // someone else handled it first; a normal outcome
	CodeUnauthorized    Code = "unauthorized"
	CodeForbidden       Code = "forbidden"
	CodeBudget          Code = "budget_exhausted"
	CodeLeaseLost       Code = "lease_lost"
	CodeUnavailable     Code = "unavailable"
	CodeInternal        Code = "internal"
)

// Error carries a code, a human-readable message and an optional cause.
type Error struct {
	Code    Code
	Message string
	Cause   error
	Details map[string]any
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// New builds an error with the given code.
func New(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code and message to a cause.
func Wrap(code Code, cause error, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// With returns a copy of e with a detail attached.
func (e *Error) With(key string, v any) *Error {
	c := *e
	c.Details = make(map[string]any, len(e.Details)+1)
	for k, val := range e.Details {
		c.Details[k] = val
	}
	c.Details[key] = v
	return &c
}

// CodeOf returns the code of err, or CodeInternal if it carries none.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Is reports whether err carries code.
func Is(err error, code Code) bool { return err != nil && CodeOf(err) == code }

// Display renders err for a person: the message plus everything wrapped under
// it, without the leading code. TurnError messages use this so an operator
// sees "model call failed: invalid: …" rather than only the outer message.
func Display(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if !errors.As(err, &e) {
		return err.Error()
	}
	return strings.TrimPrefix(err.Error(), string(e.Code)+": ")
}

func NotFound(what, id string) *Error {
	return New(CodeNotFound, "%s %q not found", what, id)
}

func Invalid(format string, args ...any) *Error { return New(CodeInvalid, format, args...) }

func Conflict(format string, args ...any) *Error { return New(CodeConflict, format, args...) }
