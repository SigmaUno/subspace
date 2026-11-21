package store

func TraceNote(note, traceID string) string {
	switch {
	case traceID == "":
		return note
	case note == "":
		return "trace=" + traceID
	}
	return note + "; trace=" + traceID
}
