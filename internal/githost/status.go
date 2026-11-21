package githost

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

const (
	StatePending = "pending"
	StateSuccess = "success"
	StateFailure = "failure"
	StateError   = "error"
)

type CommitStatus struct {
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
}

var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

func (s CommitStatus) Validate(sha string) error {
	if !shaRe.MatchString(sha) {
		return fmt.Errorf("githost: invalid commit sha %q", sha)
	}
	switch s.State {
	case StatePending, StateSuccess, StateFailure, StateError:
	default:
		return fmt.Errorf("githost: invalid status state %q", s.State)
	}
	if s.Context == "" {
		return fmt.Errorf("githost: status context is required")
	}
	return nil
}

type StatusError struct {
	Code       int
	RetryAfter time.Duration
	ResetAt    time.Time
	Limited    bool
}

func (e *StatusError) Error() string { return fmt.Sprintf("githost: unexpected status %d", e.Code) }

func (e *StatusError) Retryable() bool {
	return e.Limited || e.Code == http.StatusTooManyRequests || e.Code >= 500
}

func NewStatusError(resp *http.Response) *StatusError {
	e := &StatusError{Code: resp.StatusCode}
	e.Limited = resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0")
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && r > 0 {
		e.ResetAt = time.Unix(r, 0)
	}
	return e
}
