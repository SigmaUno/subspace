package transcript

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/SigmaUno/subspace/internal/fsx"
)

var taskIDRe = regexp.MustCompile(`^T-[0-9]+$`)

type Entry struct {
	Offset  int64     `json:"offset"`
	TS      time.Time `json:"ts"`
	AgentID string    `json:"agent_id"`
	Stream  string    `json:"stream"`
	Text    string    `json:"text"`
}

func segmentName(task string, first int64) string {
	return filepath.ToSlash(filepath.Join(task, fmt.Sprintf("%s-%012d.ndjson.gz", task, first)))
}

func encode(entries []Entry) (raw int64, gz []byte, sum string, err error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	enc := json.NewEncoder(zw)
	enc.SetEscapeHTML(false)
	for _, e := range entries {
		if err = enc.Encode(e); err != nil {
			return 0, nil, "", err
		}
	}
	if err = zw.Close(); err != nil {
		return 0, nil, "", err
	}
	for _, e := range entries {
		raw += int64(len(e.Text))
	}
	return raw, buf.Bytes(), fsx.SHA256Hex(buf.Bytes()), nil
}

func writeFile(dir, name string, data []byte) error {
	return fsx.WriteAtomic(filepath.Join(dir, filepath.FromSlash(name)), data)
}

func readFile(dir string, seg Segment) ([]byte, error) {
	raw, err := fsx.ReadVerified(filepath.Join(dir, filepath.FromSlash(seg.File)), seg.SHA256, seg.Bytes)
	if err != nil {
		return nil, fmt.Errorf("transcript segment %s: %w", seg.File, err)
	}
	return raw, nil
}

func decode(raw []byte, seg Segment) ([]Entry, error) {
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("transcript segment %s: %w", seg.File, err)
	}
	var out []Entry
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("transcript segment %s line %d: %w", seg.File, len(out)+1, err)
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("transcript segment %s: %w", seg.File, err)
	}
	if len(out) != seg.Lines {
		return nil, fmt.Errorf("transcript segment %s: %d lines, manifest says %d", seg.File, len(out), seg.Lines)
	}
	return out, nil
}
