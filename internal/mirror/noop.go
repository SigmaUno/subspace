package mirror

import "context"

// NoopMirror is an IssueMirror that does nothing.
type NoopMirror struct{}

var _ IssueMirror = NoopMirror{}

// Upsert implements IssueMirror.
func (NoopMirror) Upsert(context.Context, TaskView) (string, error) { return "", nil }

// Close implements IssueMirror.
func (NoopMirror) Close(context.Context, string) error { return nil }
