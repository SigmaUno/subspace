package main

import "testing"

func TestHAStatusCommand(t *testing.T) {
	h := newHarness(t)
	status := h.object("ha", "status")
	if status["instance"] == "" || status["backend"] == nil || status["events"] == nil {
		t.Fatalf("ha status = %v", status)
	}
	h.fails(exitUsage, "ha", "status", "extra")
	h.fails(exitUsage, "ha")
}
