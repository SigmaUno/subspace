package openapi

import (
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/SigmaUno/subspace/internal/retention"
)

type Free struct{}

type Binary []byte

var (
	timeType     = reflect.TypeFor[time.Time]()
	rawType      = reflect.TypeFor[json.RawMessage]()
	freeType     = reflect.TypeFor[Free]()
	binaryType   = reflect.TypeFor[Binary]()
	durationType = reflect.TypeFor[retention.Duration]()
)

type schemas struct {
	defs  map[string]any
	names map[reflect.Type]string
	owner map[string]reflect.Type
}

func newSchemas() *schemas {
	return &schemas{defs: map[string]any{}, names: map[reflect.Type]string{}, owner: map[string]reflect.Type{}}
}

func (s *schemas) of(v any) map[string]any {
	return s.typeOf(reflect.TypeOf(v))
}

func (s *schemas) typeOf(t reflect.Type) map[string]any {
	switch t {
	case timeType:
		return map[string]any{"type": "string", "format": "date-time"}
	case durationType:
		return map[string]any{"type": "string"}
	case rawType, freeType:
		return map[string]any{}
	case binaryType:
		return map[string]any{"type": "string", "format": "binary"}
	}
	switch t.Kind() {
	case reflect.Pointer:
		return nullable(s.typeOf(t.Elem()))
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": []string{"string", "null"}, "contentEncoding": "base64"}
		}
		return map[string]any{"type": []string{"array", "null"}, "items": s.typeOf(t.Elem())}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic(fmt.Sprintf("openapi: map key %s is not a string", t.Key()))
		}
		return map[string]any{"type": []string{"object", "null"}, "additionalProperties": s.typeOf(t.Elem())}
	case reflect.Struct:
		return s.object(t)
	}
	panic(fmt.Sprintf("openapi: unsupported kind %s", t.Kind()))
}

func nullable(sc map[string]any) map[string]any {
	if _, ok := sc["$ref"]; ok {
		return map[string]any{"oneOf": []any{sc, map[string]any{"type": "null"}}}
	}
	ty, ok := sc["type"].(string)
	if !ok {
		return sc
	}
	out := make(map[string]any, len(sc))
	for k, v := range sc {
		out[k] = v
	}
	out["type"] = []string{ty, "null"}
	return out
}

func (s *schemas) object(t reflect.Type) map[string]any {
	if t.Name() == "" {
		return s.body(t)
	}
	name, ok := s.names[t]
	if !ok {
		name = s.nameFor(t)
		s.names[t] = name
		s.owner[name] = t
		s.defs[name] = map[string]any{}
		s.defs[name] = s.body(t)
	}
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func (s *schemas) nameFor(t reflect.Type) string {
	base := sanitize(t.Name())
	if n, ok := componentNames[t]; ok {
		base = n
	}
	if _, taken := s.owner[base]; !taken {
		return base
	}
	pkg := path.Base(t.PkgPath())
	prefixed := sanitize(strings.ToUpper(pkg[:1]) + pkg[1:] + base)
	if _, taken := s.owner[prefixed]; !taken {
		return prefixed
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s%d", prefixed, i)
		if _, taken := s.owner[n]; !taken {
			return n
		}
	}
}

func sanitize(n string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, n)
}

type field struct {
	name     string
	typ      reflect.Type
	optional bool
	depth    int
}

func collect(t reflect.Type, depth int) []field {
	var out []field
	for i := range t.NumField() {
		sf := t.Field(i)
		tag, hasTag := sf.Tag.Lookup("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if sf.Anonymous && name == "" {
			et := sf.Type
			if et.Kind() == reflect.Pointer {
				et = et.Elem()
			}
			if et.Kind() == reflect.Struct {
				out = append(out, collect(et, depth+1)...)
				continue
			}
		}
		if !sf.IsExported() {
			continue
		}
		if !hasTag || name == "" {
			name = sf.Name
		}
		out = append(out, field{name: name, typ: sf.Type, optional: strings.Contains(","+opts+",", ",omitempty,"), depth: depth})
	}
	return out
}

func (s *schemas) body(t reflect.Type) map[string]any {
	best := map[string]field{}
	for _, f := range collect(t, 0) {
		if prev, ok := best[f.name]; !ok || f.depth < prev.depth {
			best[f.name] = f
		}
	}
	props := make(map[string]any, len(best))
	var required []string
	for name, f := range best {
		props[name] = s.typeOf(f.typ)
		if !f.optional {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
