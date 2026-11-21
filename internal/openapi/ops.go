package openapi

import (
	"net/http"
	"reflect"
	"strconv"
	"strings"
)

const (
	mediaJSON = "application/json"
	apiPrefix = "/api/v1"
)

type Op struct {
	Method   string
	Path     string
	Summary  string
	Query    string
	Header   string
	Request  any
	Bodies   map[string]any
	Status   int
	Response any
	Media    string
	Public   bool
	Security string
}

func get(path, summary string, resp any) Op {
	return Op{Method: http.MethodGet, Path: path, Summary: summary, Status: http.StatusOK, Response: resp}
}

func del(path, summary string, resp any) Op {
	return Op{Method: http.MethodDelete, Path: path, Summary: summary, Status: http.StatusOK, Response: resp}
}

func send(method, path, summary string, req, resp any) Op {
	return Op{Method: method, Path: path, Summary: summary, Request: req, Status: http.StatusOK, Response: resp}
}

func post(path, summary string, req, resp any) Op {
	return send(http.MethodPost, path, summary, req, resp)
}

func put(path, summary string, req, resp any) Op {
	return send(http.MethodPut, path, summary, req, resp)
}

func patch(path, summary string, req, resp any) Op {
	return send(http.MethodPatch, path, summary, req, resp)
}

func (o Op) Q(spec string) Op { o.Query = spec; return o }

func (o Op) H(spec string) Op { o.Header = spec; return o }

func (o Op) S(code int) Op { o.Status = code; return o }

func (o Op) As(media string) Op { o.Media = media; return o }

func (o Op) Body(media string, sample any) Op {
	bodies := make(map[string]any, len(o.Bodies)+1)
	for k, v := range o.Bodies {
		bodies[k] = v
	}
	bodies[media] = sample
	o.Bodies = bodies
	return o
}

func (o Op) Open() Op { o.Public = true; return o }

func (o Op) Signed(scheme string) Op { o.Public = true; o.Security = scheme; return o }

func obj(kv ...any) any {
	fields := make([]reflect.StructField, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key := kv[i].(string)
		fields = append(fields, reflect.StructField{
			Name: "F" + strconv.Itoa(i/2),
			Type: reflect.TypeOf(kv[i+1]),
			Tag:  reflect.StructTag(`json:"` + key + `"`),
		})
	}
	return reflect.New(reflect.StructOf(fields)).Elem().Interface()
}

func list[T any](key string) any { return obj(key, []T{}) }

func (o Op) ID() string {
	var b strings.Builder
	b.WriteString(strings.ToLower(o.Method))
	for _, seg := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(o.Path, apiPrefix), "/"), "/") {
		if seg == "" {
			continue
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			b.WriteString("By")
			seg = seg[1 : len(seg)-1]
		}
		b.WriteString(pascal(seg))
	}
	return b.String()
}

func pascal(s string) string {
	var b strings.Builder
	up := true
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9':
			if up && r >= 'a' && r <= 'z' {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
			up = false
		default:
			up = true
		}
	}
	return b.String()
}

func (o Op) Tag() string {
	p := strings.TrimPrefix(o.Path, "/")
	if rest, ok := strings.CutPrefix(o.Path, apiPrefix+"/"); ok {
		p = rest
	}
	seg, _, _ := strings.Cut(p, "/")
	if seg == "" {
		return "system"
	}
	switch seg {
	case "healthz", "state", "pause-all", "resume-all", "charter", "backup", "backups", "limits", "metrics", "events", "openapi.json":
		return "system"
	case "a":
		return "action-links"
	}
	return seg
}

func Routes() []Op { return append([]Op(nil), operations...) }
