package openapi_test

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

type change struct {
	key      string
	breaking bool
}

type direction string

const (
	request  direction = "request"
	response direction = "response"
)

const jsonMedia = "application/json"

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

type differ struct {
	oldRoot, curRoot map[string]any
	seen             map[string]bool
	out              []change
}

func (d *differ) breaking(format string, args ...any) {
	d.out = append(d.out, change{key: fmt.Sprintf(format, args...), breaking: true})
}

func (d *differ) additive(format string, args ...any) {
	d.out = append(d.out, change{key: fmt.Sprintf(format, args...)})
}

func (d *differ) result() []change {
	sort.Slice(d.out, func(i, j int) bool { return d.out[i].key < d.out[j].key })
	return d.out
}

func classifyDocs(old, cur map[string]any) []change {
	d := &differ{oldRoot: old, curRoot: cur, seen: map[string]bool{}}
	oldPaths, curPaths := obj(old["paths"]), obj(cur["paths"])
	for _, p := range sortedKeys(oldPaths) {
		for _, m := range httpMethods {
			o := obj(obj(oldPaths[p])[m])
			if o == nil {
				continue
			}
			key := strings.ToUpper(m) + " " + p
			n := obj(obj(curPaths[p])[m])
			if n == nil {
				d.breaking("removed operation %s", key)
				continue
			}
			d.operation(key, o, n)
		}
	}
	for _, p := range sortedKeys(curPaths) {
		for _, m := range httpMethods {
			if obj(obj(curPaths[p])[m]) != nil && obj(obj(oldPaths[p])[m]) == nil {
				d.additive("added operation %s %s", strings.ToUpper(m), p)
			}
		}
	}
	return d.result()
}

func classifyTools(old, cur map[string]any) []change {
	d := &differ{seen: map[string]bool{}}
	for _, role := range sortedKeys(old) {
		oldTools, curTools := obj(old[role]), obj(cur[role])
		for _, name := range sortedKeys(oldTools) {
			key := "tool " + role + "/" + name
			n := obj(curTools[name])
			if n == nil {
				d.breaking("removed %s", key)
				continue
			}
			d.walk(key, obj(oldTools[name]), n, request)
		}
	}
	for _, role := range sortedKeys(cur) {
		for _, name := range sortedKeys(obj(cur[role])) {
			if obj(obj(old[role])[name]) == nil {
				d.additive("added tool %s/%s", role, name)
			}
		}
	}
	return d.result()
}

func (d *differ) operation(key string, o, n map[string]any) {
	d.parameters(key, o, n)
	d.requestBody(key, o, n)
	d.responses(key, obj(o["responses"]), obj(n["responses"]))
}

func (d *differ) parameters(key string, o, n map[string]any) {
	old := map[string]bool{}
	for _, p := range list(o["parameters"]) {
		old[paramID(obj(p))] = true
	}
	for _, p := range list(n["parameters"]) {
		pm := obj(p)
		if old[paramID(pm)] {
			continue
		}
		if pm["required"] == true {
			d.breaking("new required parameter %s of %s", paramID(pm), key)
		} else {
			d.additive("added parameter %s of %s", paramID(pm), key)
		}
	}
}

func paramID(p map[string]any) string { return fmt.Sprint(p["in"], ":", p["name"]) }

func (d *differ) requestBody(key string, o, n map[string]any) {
	oldBody := obj(obj(o["requestBody"])["content"])
	curBody := obj(obj(n["requestBody"])["content"])
	for _, media := range sortedKeys(curBody) {
		loc := key + " request" + mediaSuffix(media)
		cur := obj(obj(curBody[media])["schema"])
		old := obj(obj(oldBody[media])["schema"])
		if old == nil {
			old = map[string]any{"type": "object"}
		}
		d.walk(loc, old, cur, request)
	}
}

func (d *differ) responses(key string, o, n map[string]any) {
	var removed, added []string
	for _, code := range sortedKeys(o) {
		if n[code] == nil && code != "default" {
			removed = append(removed, code)
		}
	}
	for _, code := range sortedKeys(n) {
		if o[code] == nil && code != "default" {
			added = append(added, code)
		}
	}
	for len(removed) > 0 && len(added) > 0 {
		d.breaking("changed status code of %s: %s -> %s", key, removed[0], added[0])
		removed, added = removed[1:], added[1:]
	}
	for _, code := range removed {
		d.breaking("removed response status %s of %s", code, key)
	}
	for _, code := range added {
		d.additive("added response status %s of %s", code, key)
	}
	for _, code := range sortedKeys(o) {
		if n[code] == nil || code == "default" {
			continue
		}
		oldContent := obj(obj(o[code])["content"])
		curContent := obj(obj(n[code])["content"])
		for _, media := range sortedKeys(oldContent) {
			loc := fmt.Sprintf("%s %s%s", key, code, mediaSuffix(media))
			if curContent[media] == nil {
				d.breaking("removed response body %s", loc)
				continue
			}
			d.walk(loc, obj(obj(oldContent[media])["schema"]), obj(obj(curContent[media])["schema"]), response)
		}
	}
}

func mediaSuffix(media string) string {
	if media == jsonMedia {
		return ""
	}
	return " " + media
}

func (d *differ) resolve(root, node map[string]any) (string, map[string]any) {
	ref, _ := node["$ref"].(string)
	name, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok {
		return "", node
	}
	return name, obj(obj(obj(root["components"])["schemas"])[name])
}

func (d *differ) walk(loc string, o, n map[string]any, dir direction) {
	if o == nil || n == nil {
		return
	}
	oldName, o := d.resolve(d.oldRoot, o)
	curName, n := d.resolve(d.curRoot, n)
	if curName != "" {
		visit := string(dir) + " " + curName + " " + oldName
		if d.seen[visit] {
			return
		}
		d.seen[visit] = true
		loc = curName
	}
	if o == nil || n == nil {
		return
	}
	d.types(loc, o, n, dir)
	if dir == request {
		d.enums(loc, o, n)
	}
	d.properties(loc, o, n, dir)
	d.walk(loc+"[]", obj(o["items"]), obj(n["items"]), dir)
	d.walk(loc+"{}", obj(o["additionalProperties"]), obj(n["additionalProperties"]), dir)
	for _, k := range []string{"oneOf", "anyOf"} {
		d.alternatives(loc, k, o, n, dir)
	}
}

func (d *differ) alternatives(loc, kind string, o, n map[string]any, dir direction) {
	oldAlts, curAlts := list(o[kind]), list(n[kind])
	if len(oldAlts) == len(curAlts) {
		for i := range oldAlts {
			d.walk(fmt.Sprintf("%s|%d", loc, i), obj(oldAlts[i]), obj(curAlts[i]), dir)
		}
		return
	}
	if (dir == response && len(curAlts) > len(oldAlts)) || (dir == request && len(curAlts) < len(oldAlts)) {
		d.breaking("retyped %s field %s: %s alternatives changed", dir, loc, kind)
	}
}

func (d *differ) types(loc string, o, n map[string]any, dir direction) {
	ot, nt := typeSet(o), typeSet(n)
	narrower, wider := nt, ot
	if dir == request {
		narrower, wider = ot, nt
	}
	if !subset(narrower, wider) {
		d.breaking("retyped %s field %s: %s -> %s", dir, loc, typeName(ot), typeName(nt))
	}
}

func typeSet(sc map[string]any) []string {
	var out []string
	switch t := sc["type"].(type) {
	case string:
		out = []string{t}
	case []any:
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
	}
	sort.Strings(out)
	return out
}

func typeName(t []string) string {
	if len(t) == 0 {
		return "any"
	}
	return strings.Join(t, "|")
}

func subset(a, b []string) bool {
	if len(b) == 0 {
		return true
	}
	if len(a) == 0 {
		return false
	}
	for _, x := range a {
		if !slices.Contains(b, x) {
			return false
		}
	}
	return true
}

func (d *differ) enums(loc string, o, n map[string]any) {
	oldEnum, curEnum := enumValues(o), enumValues(n)
	if curEnum == nil {
		return
	}
	if oldEnum == nil {
		d.breaking("narrowed enum %s: restricted to %s", loc, strings.Join(curEnum, ", "))
		return
	}
	for _, v := range oldEnum {
		if !slices.Contains(curEnum, v) {
			d.breaking("narrowed enum %s: removed %s", loc, v)
		}
	}
}

func enumValues(sc map[string]any) []string {
	raw, ok := sc["enum"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, fmt.Sprint(v))
	}
	return out
}

func (d *differ) properties(loc string, o, n map[string]any, dir direction) {
	oldProps, curProps := obj(o["properties"]), obj(n["properties"])
	oldReq, curReq := names(o["required"]), names(n["required"])
	for _, name := range sortedKeys(oldProps) {
		field := loc + "." + name
		if curProps[name] == nil {
			if dir == response {
				d.breaking("removed response field %s", field)
			}
			continue
		}
		if dir == request && slices.Contains(curReq, name) && !slices.Contains(oldReq, name) {
			d.breaking("new required request field %s", field)
		}
		d.walk(field, obj(oldProps[name]), obj(curProps[name]), dir)
	}
	for _, name := range sortedKeys(curProps) {
		if oldProps[name] != nil {
			continue
		}
		field := loc + "." + name
		if dir == request && slices.Contains(curReq, name) {
			d.breaking("new required request field %s", field)
		} else {
			d.additive("added %s field %s", dir, field)
		}
	}
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func names(v any) []string {
	var out []string
	for _, x := range list(v) {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseAllowlist(text string) (map[string]string, error) {
	allow := map[string]string{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, reason, ok := strings.Cut(line, " :: ")
		key, reason = strings.TrimSpace(key), strings.TrimSpace(reason)
		if !ok || key == "" || reason == "" {
			return nil, fmt.Errorf("line %d: want \"<change> :: <reason>\"", i+1)
		}
		allow[key] = reason
	}
	return allow, nil
}

func unallowed(changes []change, allow map[string]string) []string {
	var out []string
	for _, c := range changes {
		if _, ok := allow[c.key]; c.breaking && !ok {
			out = append(out, c.key)
		}
	}
	return out
}
