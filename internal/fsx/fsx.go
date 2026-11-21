package fsx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".segment-*")
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
	if d, err := os.Open(dir); err == nil { //nolint:gosec // directory of the file just written
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func ReadVerified(path, sum string, size int64) ([]byte, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // caller passes a manifest path under its own directory
	if err != nil {
		return nil, err
	}
	if got := SHA256Hex(raw); got != sum || int64(len(raw)) != size {
		return nil, fmt.Errorf("sha256 %s does not match manifest %s", got, sum)
	}
	return raw, nil
}
