package main

import (
	"errors"
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
	"create": parseProfileCreate,
	"delete": func(args []string, _ io.Reader) (command, error) {
		name, err := parseID("profiles delete", args)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodDelete, path: "agent-profiles/" + name}), nil
	},
}

func parseProfileCreate(args []string, _ io.Reader) (command, error) {
	set := flags("profiles create")
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
		return nil, errors.New("profiles create requires --kind")
	}
	return call(request{method: http.MethodPost, path: "agent-profiles", body: map[string]any{
		"name": name, "role": *role, "kind": *kind, "model": *model, "provider": *provider,
		"expires_in": *expires, "count": *count, "callback_url": *callback, "description": *description,
	}}), nil
}
