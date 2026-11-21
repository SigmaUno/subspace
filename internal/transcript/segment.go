package transcript

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
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
	h := sha256.Sum256(buf.Bytes())
	return raw, buf.Bytes(), hex.EncodeToString(h[:]), nil
}

func writeFile(dir, name string, data []byte) error {
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".segment-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func readFile(dir string, seg Segment) ([]byte, error) {
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(seg.File))) //nolint:gosec // manifest path under the transcript dir
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != seg.SHA256 || int64(len(raw)) != seg.Bytes {
		return nil, fmt.Errorf("transcript segment %s: sha256 %s does not match manifest %s", seg.File, got, seg.SHA256)
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
