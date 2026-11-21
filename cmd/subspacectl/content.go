package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	validArtifactScope = regexp.MustCompile(`^(agent|task|project):[a-zA-Z0-9_-]+$`)
	validScope         = regexp.MustCompile(`^(global|project:[a-zA-Z0-9_-]+)$`)
	instructionKinds   = []string{"rules", "style", "templates"}
)

var artifactCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("artifacts list")
		scope := set.String("scope", "", "filter by scope")
		limit := set.Int("limit", 0, "maximum results")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		q, err := limited(*limit, 1000)
		if err != nil {
			return nil, err
		}
		return get("artifacts", setIf(q, "scope", *scope)), nil
	},
	"upload":   parseUpload,
	"download": parseDownload,
	"delete": func(args []string, _ io.Reader) (command, error) {
		id, err := parseID("artifacts delete", args)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodDelete, path: "artifacts/" + id}), nil
	},
}

func parseUpload(args []string, stdin io.Reader) (command, error) {
	set := flags("artifacts upload")
	scope := set.String("scope", "", "agent:ID, task:ID or project:ID")
	file := set.String("file", "", "file to upload or - for stdin")
	name := set.String("name", "", "artifact name (default: the file name)")
	contentType := set.String("content-type", "", "content type (default: from the file extension)")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	if _, err := matching(validArtifactScope, "scope", *scope); err != nil {
		return nil, err
	}
	if *file == "" || (*file == "-" && *name == "") {
		return nil, fmt.Errorf("artifacts upload requires --file, and --name when reading stdin")
	}
	data, err := readInput(*file, stdin)
	if err != nil {
		return nil, fmt.Errorf("read upload: %w", err)
	}
	filename := *name
	if filename == "" {
		filename = filepath.Base(*file)
	}
	ctype := *contentType
	if ctype == "" {
		ctype = mime.TypeByExtension(filepath.Ext(filename))
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	payload, formType, err := multipartBody(*scope, *name, filename, ctype, data)
	if err != nil {
		return nil, err
	}
	return call(request{
		method: http.MethodPost, path: "artifacts", payload: payload, contentType: formType,
		timeout: transferTimeout,
	}), nil
}

func multipartBody(scope, name, filename, contentType string, data []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("scope", scope); err != nil {
		return nil, "", err
	}
	if name != "" {
		if err := w.WriteField("name", name); err != nil {
			return nil, "", err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
	header.Set("Content-Type", contentType)
	part, err := w.CreatePart(header)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(data); err != nil {
		return nil, "", err
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}

func parseDownload(args []string, _ io.Reader) (command, error) {
	set := flags("artifacts download")
	out := set.String("out", "", "destination file or - for stdout")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	if *out == "" {
		return nil, fmt.Errorf("artifacts download requires --out")
	}
	req := request{
		method: http.MethodGet, path: "artifacts/" + id, accept: "*/*",
		maxResponse: transferMax, timeout: transferTimeout,
	}
	return func(ctx context.Context, c *conn, p *printer) error {
		resp, err := c.do(ctx, req)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(resp.body)
		digest := hex.EncodeToString(sum[:])
		if tag := strings.Trim(resp.header.Get("ETag"), `"`); tag != "" && tag != digest {
			return failf(exitFailure, "downloaded data does not match the server checksum")
		}
		return save(*out, resp.body, p, map[string]any{"id": id, "sha256": digest})
	}, nil
}

var instructionCommands = table{
	"get": func(args []string, _ io.Reader) (command, error) {
		set := flags("instructions get")
		scope := set.String("scope", "global", "global or project:ID")
		kind := set.String("kind", "", "rules, style or templates (default: the effective text)")
		project := set.String("project", "", "project for the effective text")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		if *kind == "" {
			return get("instructions/effective", setIf(url.Values{}, "project", *project)), nil
		}
		path, err := instructionPath(*scope, *kind)
		if err != nil {
			return nil, err
		}
		return get(path, nil), nil
	},
	"set": func(args []string, stdin io.Reader) (command, error) {
		set := flags("instructions set")
		scope := set.String("scope", "global", "global or project:ID")
		kind := set.String("kind", "", "rules, style or templates")
		text := set.String("text", "", "instruction text")
		file := set.String("text-file", "", "read the text from file or stdin")
		message := set.String("message", "", "version message")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		path, err := instructionPath(*scope, *kind)
		if err != nil {
			return nil, err
		}
		body, err := textInput("text", *text, *file, stdin)
		if err != nil {
			return nil, err
		}
		return call(request{method: http.MethodPut, path: path, body: map[string]string{"text": body, "message": *message}}), nil
	},
}

func instructionPath(scope, kind string) (string, error) {
	if _, err := matching(validScope, "scope", scope); err != nil {
		return "", err
	}
	if !slices.Contains(instructionKinds, kind) {
		return "", fmt.Errorf("--kind must be one of %s", strings.Join(instructionKinds, ", "))
	}
	return "instructions/" + scope + "/" + kind, nil
}

var promptCommands = table{
	"list": func(args []string, _ io.Reader) (command, error) {
		set := flags("prompts list")
		tag := set.String("tag", "", "filter by tag")
		if err := parseFlags(set, args, 0, 0); err != nil {
			return nil, err
		}
		return get("prompts", setIf(url.Values{}, "tag", *tag)), nil
	},
	"show": byID("prompts show", "prompts/%s"),
	"render": func(args []string, _ io.Reader) (command, error) {
		set := flags("prompts render")
		var vars listFlag
		set.Var(&vars, "var", "template variable as name=value (repeatable)")
		if err := parseFlags(set, args, 1, 1); err != nil {
			return nil, err
		}
		id, err := identifier(set.Arg(0))
		if err != nil {
			return nil, err
		}
		q, err := parseVars(vars)
		if err != nil {
			return nil, err
		}
		return get("prompts/"+id+"/render", q), nil
	},
}

var briefCommands = table{
	"show":         byID("briefs show", "briefs/%s"),
	"architecture": byID("briefs architecture", "briefs/%s/architecture"),
	"update":       jsonBody("briefs update", http.MethodPatch, "briefs/%s", true),
	"leader":       parseBriefLeader,
	"components": grouped("briefs components", table{
		"set": jsonBody("briefs components set", http.MethodPut, "briefs/%s/components", true),
	}),
}

func parseBriefs(args []string, stdin io.Reader) (command, error) {
	if len(args) > 0 {
		switch args[0] {
		case "import":
			return parseImport(args[1:], stdin)
		case "templates":
			return parseTemplates(args[1:])
		}
		if p, ok := briefCommands[args[0]]; ok {
			return p(args[1:], stdin)
		}
	}
	return single(parseList("briefs", args))
}

func parseBriefLeader(args []string, _ io.Reader) (command, error) {
	set := flags("briefs leader")
	agent := set.String("agent", "", "leader to hand the brief to; empty = any leader")
	if err := parseFlags(set, args, 1, 1); err != nil {
		return nil, err
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	if *agent != "" {
		if _, err := identifier(*agent); err != nil {
			return nil, err
		}
	}
	return call(request{method: http.MethodPost, path: "briefs/" + id + "/leader", body: map[string]string{"agent_id": *agent}}), nil
}

func parseImport(args []string, stdin io.Reader) (command, error) {
	set := flags("briefs import")
	file := set.String("file", "", "brief document or - for stdin")
	dryRun := set.Bool("dry-run", false, "validate without creating anything")
	if err := parseFlags(set, args, 0, 0); err != nil {
		return nil, err
	}
	if *file == "" {
		return nil, fmt.Errorf("briefs import requires --file")
	}
	data, err := readInput(*file, stdin)
	if err != nil {
		return nil, fmt.Errorf("read brief document: %w", err)
	}
	q := url.Values{}
	if *dryRun {
		q.Set("dry_run", "true")
	}
	return call(request{method: http.MethodPost, path: "briefs/import", query: q, payload: data, contentType: "text/markdown"}), nil
}

func parseTemplates(args []string) (command, error) {
	set := flags("briefs templates")
	var vars listFlag
	set.Var(&vars, "var", "template variable as name=value (repeatable)")
	if err := parseFlags(set, args, 0, 1); err != nil {
		return nil, err
	}
	if set.NArg() == 0 {
		if len(vars) > 0 {
			return nil, fmt.Errorf("--var needs a template ID")
		}
		return get("brief-templates", nil), nil
	}
	id, err := identifier(set.Arg(0))
	if err != nil {
		return nil, err
	}
	q, err := parseVars(vars)
	if err != nil {
		return nil, err
	}
	return get("brief-templates/"+id+"/render", q), nil
}
