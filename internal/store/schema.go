package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
)

var ErrSchemaNewer = errors.New("store: database schema is newer than this binary")

var ErrReadOnly = errors.New("store: read-only, the database schema is newer than this binary")

type OpenOptions struct {
	ReadOnlyNewer bool
}

type Schema struct {
	Version int64
	Hash    string
}

var ddlStatement = regexp.MustCompile(`(?im)(?:^|;)\s*(?:CREATE|ALTER|DROP)\s`)

func SchemaOf(units ...string) Schema {
	h := sha256.New()
	var n int64
	for _, u := range units {
		n += max(1, int64(len(ddlStatement.FindAllStringIndex(u, -1))))
		h.Write([]byte(u))
		h.Write([]byte{0})
	}
	return Schema{Version: n, Hash: hex.EncodeToString(h.Sum(nil))}
}

type SchemaAction int

const (
	SchemaMigrate SchemaAction = iota
	SchemaCurrent
	SchemaReadOnly
)

func CheckSchema(stored *Schema, binary Schema, o OpenOptions) (SchemaAction, error) {
	switch {
	case stored == nil:
		return SchemaMigrate, nil
	case stored.Version > binary.Version && o.ReadOnlyNewer:
		return SchemaReadOnly, nil
	case stored.Version > binary.Version:
		return 0, fmt.Errorf("%w: database is at schema version %d, this binary knows %d; upgrade the binary, or set SUBSPACE_SCHEMA_READONLY=true to open it read-only for maintenance",
			ErrSchemaNewer, stored.Version, binary.Version)
	case *stored == binary:
		return SchemaCurrent, nil
	}
	return SchemaMigrate, nil
}
