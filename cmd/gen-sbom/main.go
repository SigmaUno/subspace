package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
)

type goModule struct {
	Path    string
	Version string
	Main    bool
	Replace *goModule
}

type hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type component struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	PURL    string `json:"purl,omitempty"`
	Hashes  []hash `json:"hashes,omitempty"`
}

type bom struct {
	BOMFormat    string      `json:"bomFormat"`
	SpecVersion  string      `json:"specVersion"`
	SerialNumber string      `json:"serialNumber"`
	Version      int         `json:"version"`
	Metadata     metadata    `json:"metadata"`
	Components   []component `json:"components"`
}

type metadata struct {
	Component *component `json:"component,omitempty"`
}

var (
	lockEntry     = regexp.MustCompile(`^  '?([^'\s][^']*?)'?:$`)
	lockIntegrity = regexp.MustCompile(`integrity: (sha\d+)-([A-Za-z0-9+/=]+)`)
	hashNames     = map[string]string{"sha1": "SHA-1", "sha256": "SHA-256", "sha384": "SHA-384", "sha512": "SHA-512"}
)

func main() {
	lock := flag.String("lock", "web/pnpm-lock.yaml", "pnpm lockfile")
	flag.Parse()
	if err := run(os.Stdin, *lock, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "gen-sbom:", err)
		os.Exit(1)
	}
}

func run(goList io.Reader, lockPath string, out io.Writer) error {
	lockData, err := os.ReadFile(lockPath) //nolint:gosec // path chosen by the operator on the command line
	if err != nil {
		return err
	}
	npm, err := parseLock(bytes.NewReader(lockData))
	if err != nil {
		return fmt.Errorf("%s: %w", lockPath, err)
	}
	root, goComponents, err := parseGoModules(goList)
	if err != nil {
		return err
	}
	b := newBOM(root, append(goComponents, npm...))
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(b)
}

func newBOM(root *component, components []component) bom {
	slices.SortFunc(components, func(a, b component) int { return strings.Compare(a.PURL, b.PURL) })
	sum := sha256.New()
	for _, c := range components {
		sum.Write([]byte(c.PURL + "\n"))
	}
	id := hex.EncodeToString(sum.Sum(nil))
	return bom{
		BOMFormat:    "CycloneDX",
		SpecVersion:  "1.5",
		SerialNumber: fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", id[0:8], id[8:12], id[12:16], id[16:20], id[20:32]),
		Version:      1,
		Metadata:     metadata{Component: root},
		Components:   components,
	}
}

func parseGoModules(r io.Reader) (*component, []component, error) {
	var root *component
	var out []component
	dec := json.NewDecoder(r)
	for {
		var m goModule
		if err := dec.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("go list: %w", err)
		}
		if m.Main {
			root = &component{Type: "application", Name: m.Path}
			continue
		}
		if m.Replace != nil {
			m = *m.Replace
		}
		out = append(out, component{
			Type:    "library",
			Name:    m.Path,
			Version: m.Version,
			PURL:    "pkg:golang/" + m.Path + "@" + strings.ReplaceAll(m.Version, "+", "%2B"),
		})
	}
	if root == nil {
		return nil, nil, errors.New("go list: no main module in input")
	}
	return root, out, nil
}

func parseLock(r io.Reader) ([]component, error) {
	var out []component
	inPackages := false
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line != "" && line[0] != ' ' {
			inPackages = line == "packages:"
			continue
		}
		if !inPackages {
			continue
		}
		if m := lockEntry.FindStringSubmatch(line); m != nil {
			name, version, ok := splitPackage(m[1])
			if !ok {
				return nil, fmt.Errorf("unparsable package key %q", m[1])
			}
			out = append(out, component{
				Type:    "library",
				Name:    name,
				Version: version,
				PURL:    "pkg:npm/" + strings.ReplaceAll(name, "@", "%40") + "@" + strings.ReplaceAll(version, "+", "%2B"),
			})
			continue
		}
		if m := lockIntegrity.FindStringSubmatch(line); m != nil && len(out) > 0 {
			alg, ok := hashNames[m[1]]
			raw, err := base64.StdEncoding.DecodeString(m[2])
			if !ok || err != nil {
				return nil, fmt.Errorf("bad integrity %q for %s", m[0], out[len(out)-1].Name)
			}
			last := &out[len(out)-1]
			last.Hashes = append(last.Hashes, hash{Alg: alg, Content: hex.EncodeToString(raw)})
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no packages section")
	}
	return out, nil
}

func splitPackage(key string) (name, version string, ok bool) {
	i := strings.LastIndex(key, "@")
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}
