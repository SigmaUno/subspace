package openapi

import "github.com/SigmaUno/subspace/internal/transcript"

func init() {
	Register(
		get("/api/v1/tasks/{id}/transcript", "Persisted agent output of a task, oldest first; since is an exclusive line offset, at jumps to the first line at or after a time, q filters lines by text", transcript.Page{}).Q("since:integer limit:integer at q"),
		get("/api/v1/tasks/{id}/transcript/download", "Download the whole transcript of a task as gzip-compressed NDJSON", "").As("application/gzip"),
	)
}
