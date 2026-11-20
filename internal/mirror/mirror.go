// Package mirror publishes a read-only view of Subspace tasks to an external
// issue tracker.
package mirror

import "context"

// TaskView is the subset of a task that is mirrored. It is deliberately a
// local type so this package does not depend on internal/model.
type TaskView struct {
	ID         string
	Title      string
	Status     string
	Assignee   string
	PRURL      string
	BriefTitle string
	Acceptance []string
}

// IssueMirror keeps one external issue per task.
type IssueMirror interface {
	// Upsert creates or updates the issue for the task and returns its URL.
	// It is idempotent: repeated calls for the same task id never create a
	// second issue.
	Upsert(ctx context.Context, t TaskView) (issueURL string, err error)
	// Close closes the issue of the task. Closing a task without an issue is
	// not an error.
	Close(ctx context.Context, taskID string) error
}
