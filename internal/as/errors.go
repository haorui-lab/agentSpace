package as

import (
	"errors"
	"fmt"
)

const (
	CodeBadRequest   = "BAD_REQUEST"
	CodeUnauthorized = "UNAUTHORIZED"
	CodeForbidden    = "FORBIDDEN"
	CodeNotFound     = "NOT_FOUND"
	CodeConflict     = "CONFLICT"
	CodeGone         = "GONE"
	CodeTooLarge     = "PAYLOAD_TOO_LARGE"
	CodeRateLimited  = "RATE_LIMITED"
	CodeInternal     = "INTERNAL"
)

type appError struct {
	Status  int
	Code    string
	Message string
	Details any
}

func (e *appError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func badRequest(msg string) *appError {
	return &appError{Status: 400, Code: CodeBadRequest, Message: msg}
}

func unauthorized(msg string) *appError {
	return &appError{Status: 401, Code: CodeUnauthorized, Message: msg}
}

func forbidden(msg string) *appError {
	return &appError{Status: 403, Code: CodeForbidden, Message: msg}
}

func notFound(msg string) *appError {
	return &appError{Status: 404, Code: CodeNotFound, Message: msg}
}

func conflict(msg string, details any) *appError {
	return &appError{Status: 409, Code: CodeConflict, Message: msg, Details: details}
}

func gone(msg string) *appError {
	return &appError{Status: 410, Code: CodeGone, Message: msg}
}

func tooLarge(msg string) *appError {
	return &appError{Status: 413, Code: CodeTooLarge, Message: msg}
}

func rateLimited(msg string) *appError {
	return &appError{Status: 429, Code: CodeRateLimited, Message: msg}
}

func internalf(format string, args ...any) *appError {
	return &appError{Status: 500, Code: CodeInternal, Message: fmt.Sprintf(format, args...)}
}

func isCode(err error, code string) bool {
	var ae *appError
	return errors.As(err, &ae) && ae != nil && ae.Code == code
}
