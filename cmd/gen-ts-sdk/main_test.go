package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneratedSDKUpToDate(t *testing.T) {
	want, err := build()
	if err != nil {
		t.Fatal(err)
	}
	again, err := build()
	if err != nil {
		t.Fatal(err)
	}
	for name, src := range want {
		if !bytes.Equal(src, again[name]) {
			t.Fatalf("%s: generation is not deterministic", name)
		}
	}
	dir := filepath.Join("..", "..", "sdk", "ts", "src", "generated")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(want) {
		t.Fatalf("%s holds %d files, the generator emits %d", dir, len(entries), len(want))
	}
	for name, src := range want {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, src) {
			t.Fatalf("sdk/ts/src/generated/%s is stale; run `pnpm gen` in sdk/ts", name)
		}
	}
}
