package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/SigmaUno/subspace/internal/guardhint"
)

func TestGeneratedTypesUpToDate(t *testing.T) {
	guardhint.Watch(t, guardhint.TSTypes)
	want, err := generate(registry)
	if err != nil {
		t.Fatal(err)
	}
	again, err := generate(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, again) {
		t.Fatal("generation is not deterministic")
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "web", "lib", "generated-types.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("web/lib/generated-types.ts is stale; run `make gen-ts` and commit the file (types come from cmd/gen-ts/registry.go)")
	}
}
