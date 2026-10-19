// Package safeerror provides log-safe errors at boundaries handling credentials.
package safeerror

import (
	"context"
	"errors"
	"net"
	"strconv"
)

type safeError struct {
	operation string
	cause     error
}

// Wrap hides the original message, which may contain URLs or database secrets.
// operation must be a constant, never user input or a request URL.
// Unwrap preserves errors.Is/As for cancellation and retry classification.
func Wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return &safeError{operation: operation, cause: err}
}

func (e *safeError) Error() string {
	category := "operation failed"
	var networkError net.Error
	var status interface{ StatusCode() int }
	switch {
	case errors.Is(e.cause, context.Canceled):
		category = "canceled"
	case errors.Is(e.cause, context.DeadlineExceeded):
		category = "timeout"
	case errors.As(e.cause, &status):
		category = "HTTP/API status " + strconv.Itoa(status.StatusCode())
	case errors.As(e.cause, &networkError):
		if networkError.Timeout() {
			category = "timeout"
		} else {
			category = "network error"
		}
	}
	return e.operation + ": " + category
}

func (e *safeError) Unwrap() error { return e.cause }
