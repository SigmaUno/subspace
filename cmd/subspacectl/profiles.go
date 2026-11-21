package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

var profileCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("profiles list"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("agent-profiles", nil), nil
	},
	"show":   byID("profiles show", "agent-profiles/%s"),
	"create": parseProfileWrite("profiles create", http.MethodPost),
	"update": parseProfileWrite("profiles update", http.MethodPut),
	"delete": func(args []string, _ io.Reader) (command, error) {
		name, err := parseID("profiles delete", args)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodDelete, path: "agent-profiles/" + name}), nil
	},
}

func parseProfileWrite(label, method string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		set := flags(label)
		role := set.String("role", "worker", "agent role")
		kind := set.String("kind", "", "runtime kind")
		model := set.String("model", "", "model name")
		provider := set.String("provider", "", "provider (default from the kind)")
		expires := set.String("expires-in", "", "token lifetime such as 720h")
		count := set.Int("count", 0, "default bulk size")
		callback := set.String("callback-url", "", "wake-up callback URL")
		description := set.String("description", "", "what the profile is for")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		name, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(*kind) == "" {
			return nil, fmt.Errorf("%s requires --kind", label)
		}
		path := "agent-profiles"
		if method == http.MethodPut {
			path += "/" + name
		}
		return call(request{method: method, path: path, body: map[string]any{
			"name": name, "role": *role, "kind": *kind, "model": *model, "provider": *provider,
			"expires_in": *expires, "count": *count, "callback_url": *callback, "description": *description,
		}}), nil
	}
}
