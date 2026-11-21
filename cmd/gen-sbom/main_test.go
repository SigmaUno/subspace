package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goListInput = `{"Path":"example.com/app","Main":true,"Dir":"/src"}
{"Path":"example.com/zlib","Version":"v1.2.0"}
{"Path":"example.com/old","Version":"v2.0.0+incompatible","Replace":{"Path":"example.com/fork","Version":"v2.1.0"}}
`

const lockInput = `lockfileVersion: '9.0'

importers:

  .:
    dependencies:
      next:
        specifier: 16.4.6
        version: 16.4.6

packages:

  '@scope/pkg@1.0.0':
    resolution: {integrity: sha512-AAECAw==}
    engines: {node: '>=22'}

  next@16.4.6:
    resolution: {integrity: sha256-AAECAw==}

snapshots:

  '@scope/pkg@1.0.0': {}

  next@16.4.6:
    dependencies:
      '@scope/pkg': 1.0.0
`

func generate(t *testing.T, lock string) bom {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pnpm-lock.yaml")
	if err := os.WriteFile(path, []byte(lock), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(strings.NewReader(goListInput), path, &out); err != nil {
		t.Fatal(err)
	}
	var b bom
	if err := json.Unmarshal(out.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSBOMFromGoListAndLockfile(t *testing.T) {
	b := generate(t, lockInput)
	if b.BOMFormat != "CycloneDX" || b.Metadata.Component == nil || b.Metadata.Component.Name != "example.com/app" {
		t.Fatalf("unexpected header: %+v", b)
	}
	var purls []string
	for _, c := range b.Components {
		purls = append(purls, c.PURL)
	}
	want := []string{
		"pkg:golang/example.com/fork@v2.1.0",
		"pkg:golang/example.com/zlib@v1.2.0",
		"pkg:npm/%40scope/pkg@1.0.0",
		"pkg:npm/next@16.4.6",
	}
	if strings.Join(purls, " ") != strings.Join(want, " ") {
		t.Fatalf("purls = %v, want %v", purls, want)
	}
	scoped := b.Components[2]
	if len(scoped.Hashes) != 1 || scoped.Hashes[0] != (hash{Alg: "SHA-512", Content: "00010203"}) {
		t.Fatalf("hashes = %+v", scoped.Hashes)
	}
	if next := b.Components[3]; next.Hashes[0].Alg != "SHA-256" {
		t.Fatalf("hashes = %+v", next.Hashes)
	}
	if again := generate(t, lockInput); again.SerialNumber != b.SerialNumber {
		t.Fatalf("serial number differs between runs: %s vs %s", again.SerialNumber, b.SerialNumber)
	}
}

func TestSBOMRejectsLockfileWithoutPackages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pnpm-lock.yaml")
	if err := os.WriteFile(path, []byte("lockfileVersion: '9.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(strings.NewReader(goListInput), path, &bytes.Buffer{}); err == nil {
		t.Fatal("expected an error")
	}
}
