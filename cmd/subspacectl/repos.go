package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var validRepo = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

var repoCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		if err := parseFlags(flags("repos list"), args, 0, 0); err != nil {
			return nil, err
		}
		return get("repos", nil), nil
	},
	"show":   repoByName("repos show", http.MethodGet, ""),
	"health": repoByName("repos health", http.MethodGet, "/health"),
	"remove": repoByName("repos remove", http.MethodDelete, ""),
	"add":    parseRepoAdd,
	"update": parseRepoUpdate,
}

func repoByName(name, method, suffix string) parser {
	return func(args []string, _ io.Reader) (command, error) {
		repo, err := parseRepoName(flags(name), args)
		if err != nil {
			return nil, err
		}
		return call(request{method: method, path: "repos/" + repo + suffix}), nil
	}
}

type repoFlags struct {
	host, baseURL, branch, mergeTest, workDir, tokenEnv string
	protect                                             listFlag
}

func (f *repoFlags) register(set *flag.FlagSet) {
	set.StringVar(&f.host, "host", "", "git host kind: github or forgejo")
	set.StringVar(&f.baseURL, "base-url", "", "web URL of the host (default https://github.com for github)")
	set.StringVar(&f.branch, "default-branch", "", "default branch (default main)")
	set.Var(&f.protect, "protect", "protected branch pattern, repeatable")
	set.StringVar(&f.mergeTest, "merge-test", "", "merge queue test command, such as \"make test\"")
	set.StringVar(&f.workDir, "merge-workdir", "", "absolute directory of the merge queue checkouts")
	set.StringVar(&f.tokenEnv, "token-env", "", "name of the environment variable holding the host token")
}

func parseRepoAdd(args []string, _ io.Reader) (command, error) {
	set := flags("repos add")
	var f repoFlags
	f.register(set)
	disabled := set.Bool("disabled", false, "register the repository as disabled")
	name, err := parseRepoName(set, args)
	if err != nil {
		return nil, err
	}
	if f.host == "" {
		return nil, fmt.Errorf("repos add requires --host")
	}
	return call(request{method: http.MethodPost, path: "repos", body: map[string]any{
		"name": name, "host": f.host, "base_url": f.baseURL, "default_branch": f.branch,
		"protected_branches": nonNil(f.protect), "merge_test_command": strings.Fields(f.mergeTest),
		"merge_work_dir": f.workDir, "token_env": f.tokenEnv, "enabled": !*disabled,
	}}), nil
}

func parseRepoUpdate(args []string, _ io.Reader) (command, error) {
	set := flags("repos update")
	var f repoFlags
	f.register(set)
	enabled := set.Bool("enabled", true, "enable or disable the repository")
	name, err := parseRepoName(set, args)
	if err != nil {
		return nil, err
	}
	changed := map[string]bool{}
	set.Visit(func(x *flag.Flag) { changed[x.Name] = true })
	if len(changed) == 0 {
		return nil, fmt.Errorf("repos update needs at least one flag")
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, request{method: http.MethodGet, path: "repos/" + name})
		if err != nil {
			return err
		}
		var cur map[string]any
		if err := json.Unmarshal(resp.body, &cur); err != nil {
			return fmt.Errorf("decode repository: %w", err)
		}
		body := map[string]any{}
		for _, k := range []string{"name", "host", "base_url", "default_branch", "protected_branches", "merge_test_command", "merge_work_dir", "token_env", "enabled"} {
			body[k] = cur[k]
		}
		put := func(flagName, key string, v any) {
			if changed[flagName] {
				body[key] = v
			}
		}
		put("host", "host", f.host)
		put("base-url", "base_url", f.baseURL)
		put("default-branch", "default_branch", f.branch)
		put("protect", "protected_branches", nonNil(f.protect))
		put("merge-test", "merge_test_command", strings.Fields(f.mergeTest))
		put("merge-workdir", "merge_work_dir", f.workDir)
		put("token-env", "token_env", f.tokenEnv)
		put("enabled", "enabled", *enabled)
		out, err := c.do(ctx, request{method: http.MethodPut, path: "repos/" + name, body: body})
		if err != nil {
			return err
		}
		return p.response(out)
	}, nil
}

func parseRepoName(set *flag.FlagSet, args []string) (string, error) {
	if err := parseFlags(set, args, 1, 1); err != nil {
		return "", err
	}
	return matching(validRepo, "repository", set.Arg(0))
}

func nonNil(l listFlag) []string {
	if l == nil {
		return []string{}
	}
	return l
}
