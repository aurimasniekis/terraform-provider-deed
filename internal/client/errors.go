// Adapted from terraform-provider-porch internal/client/errors.go (Apache-2.0).

package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Stable problem codes returned by Deed.
const (
	CodeNotFound           = "not_found"
	CodeAlreadyExists      = "already_exists"
	CodeConflict           = "conflict"
	CodePreconditionFailed = "precondition_failed"
	CodeValidationFailed   = "validation_failed"
	CodeReauthRequired     = "reauth_required"
	CodeUnauthorized       = "unauthorized"
	CodeForbidden          = "forbidden"
)

// FieldError is a per-field validation problem. Field is a path such as `metadata.labels.team`, `spec/port` or
// `schema`.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ErrorDetail is a request-schema validation detail (Huma), e.g. location `body.metadata.displayName`.
type ErrorDetail struct {
	Message  string `json:"message"`
	Location string `json:"location,omitempty"`
}

// APIError is an RFC 9457 problem returned by Deed.
type APIError struct {
	StatusCode int           `json:"status"`
	Type       string        `json:"type,omitempty"`
	Title      string        `json:"title,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	Code       string        `json:"code"`
	Fields     []FieldError  `json:"fields,omitempty"`
	Errors     []ErrorDetail `json:"errors,omitempty"`
	Action     string        `json:"action,omitempty"`

	Method string `json:"-"`
	Path   string `json:"-"`
}

// Summary is the error without field details: "deed API POST /entities: 409 already_exists: …".
func (e *APIError) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "deed API %s %s: %d", e.Method, e.Path, e.StatusCode)
	if e.Code != "" {
		b.WriteString(" " + e.Code)
	}
	if e.Detail != "" {
		b.WriteString(": " + e.Detail)
	} else if e.Title != "" {
		b.WriteString(": " + e.Title)
	}
	return b.String()
}

func (e *APIError) Error() string {
	var parts []string
	for _, f := range e.Fields {
		parts = append(parts, f.Field+": "+f.Message)
	}
	for _, d := range e.Errors {
		if d.Location != "" {
			parts = append(parts, d.Location+": "+d.Message)
		} else {
			parts = append(parts, d.Message)
		}
	}
	if len(parts) > 0 {
		return e.Summary() + " (" + strings.Join(parts, "; ") + ")"
	}
	return e.Summary()
}

// TransportError is a network-level failure (after retries).
type TransportError struct {
	Method, Path string
	Err          error
}

func (e *TransportError) Error() string {
	return fmt.Sprintf("deed API %s %s: %v", e.Method, e.Path, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

// decodeProblem turns an error response into an *APIError. Bodies that are not problem documents are summarised by
// status only (they are never echoed, so a misbehaving proxy cannot leak data into logs).
func decodeProblem(method, path string, resp *response) *APIError {
	e := &APIError{}
	if err := json.Unmarshal(resp.body, e); err != nil || (e.Code == "" && e.Detail == "" && e.Title == "") {
		e = &APIError{Title: http.StatusText(resp.status)}
	}
	e.StatusCode = resp.status
	e.Method, e.Path = method, path
	if e.Code == "" {
		e.Code = defaultCode(resp.status)
	}
	return e
}

func defaultCode(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return CodeValidationFailed
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusConflict:
		return CodeConflict
	case http.StatusPreconditionFailed:
		return CodePreconditionFailed
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusUnauthorized:
		return CodeUnauthorized
	case http.StatusTooManyRequests:
		return "rate_limited"
	case http.StatusServiceUnavailable:
		return "unavailable"
	}
	if status >= 500 {
		return "internal"
	}
	return ""
}

// AsAPIError returns the *APIError in err's chain.
func AsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	ok := errors.As(err, &ae)
	return ae, ok
}

// IsNotFound reports whether err is a 404 problem.
func IsNotFound(err error) bool {
	ae, ok := AsAPIError(err)
	return ok && ae.StatusCode == http.StatusNotFound
}

// IsPreconditionFailed reports whether err is a 412 problem (stale If-Match).
func IsPreconditionFailed(err error) bool {
	ae, ok := AsAPIError(err)
	return ok && ae.StatusCode == http.StatusPreconditionFailed
}

// HasCode reports whether err is a problem with the given code.
func HasCode(err error, code string) bool {
	ae, ok := AsAPIError(err)
	return ok && ae.Code == code
}

// notFound is returned by lookups that scan a list and find nothing.
func notFound(path, what string) *APIError {
	return &APIError{StatusCode: http.StatusNotFound, Code: CodeNotFound, Detail: what + " not found", Method: http.MethodGet, Path: path}
}
