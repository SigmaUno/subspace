package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestDecodeDoesNotLeakTypeInfo(t *testing.T) {
	c := &Call{Args: json.RawMessage(`{"task_id":123}`)}
	var in struct {
		TaskID string `json:"task_id"`
	}
	err := decode(c, &in)
	if err == nil {
		t.Fatal("expected error")
	}
	e := model.AsError(err)
	if e.Code != "bad_request" {
		t.Fatalf("code = %q", e.Code)
	}
	for _, leak := range []string{"Go struct field", "of type", "float64", "int"} {
		if strings.Contains(e.Message, leak) {
			t.Fatalf("decode leaks internals: %q in %q", leak, e.Message)
		}
	}
	if e.Message != "invalid arguments" {
		t.Fatalf("message = %q", e.Message)
	}
}
