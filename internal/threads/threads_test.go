package threads

import (
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

func TestParseOwnerAddressesAgentNamedMe(t *testing.T) {
	r, err := Parse("agent:me", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != model.ThreadAgent || r.ID != "me" {
		t.Fatalf("got %+v, want agent me", r)
	}
}
