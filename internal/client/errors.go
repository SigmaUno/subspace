package client

import (
	"errors"
	"fmt"
	"time"
)

const (
	CodeBadRequest     = "bad_request"
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeConflict       = "conflict"
	CodePaused         = "paused"
	CodeCapacity       = "capacity"
	CodeRateLimited    = "rate_limited"
	CodeInternal       = "internal"
	CodeAgentPaused    = "agent_paused"
	CodeLeaseExpired   = "lease_expired"
	CodeNotAssigned    = "not_assigned"
	CodeBudgetExceeded = "budget_exceeded"
	CodeToolError      = "tool_error"
	CodeIdempotency    = "idempotency_conflict"
)

type Error struct {
	Status     int
	Code       string
	Message    string
	RetryAfter time.Duration
}

type TransportError struct{ Err error }

func (e *TransportError) Error() string { return e.Err.Error() }

func (e *TransportError) Unwrap() error { return e.Err }

func (e *Error) Error() string {
	switch {
	case e.Status != 0 && e.Code != "":
		return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
	case e.Status != 0 && e.Message != "":
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	case e.Status != 0:
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return e.Code + ": " + e.Message
}

func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func IsCode(err error, code string) bool { return CodeOf(err) == code }

func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 0
}

func statusCode(status int) string {
	switch status {
	case 400:
		return CodeBadRequest
	case 401:
		return CodeUnauthorized
	case 403:
		return CodeForbidden
	case 404:
		return CodeNotFound
	case 409:
		return CodeConflict
	case 423:
		return CodePaused
	case 429:
		return CodeRateLimited
	}
	return CodeInternal
}
