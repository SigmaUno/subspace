package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/model"
)

type call struct {
	status int
	ctype  string
	body   map[string]any
}

func do(t *testing.T, h http.Handler, method, target, body string) call {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	req, err := http.NewRequest(method, srv.URL+target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	c := call{status: resp.StatusCode, ctype: resp.Header.Get("Content-Type")}
	if err := json.Unmarshal(raw, &c.body); err != nil {
		t.Fatalf("body %q: %v", raw, err)
	}
	return c
}

func errorOf(t *testing.T, c call) map[string]any {
	t.Helper()
	e, ok := c.body["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error envelope: %v", c.body)
	}
	return e
}

func TestHandleWritesStatusAndErrorEnvelope(t *testing.T) {
	h := Handle(http.StatusCreated, func(r *http.Request) (any, error) {
		switch r.URL.Query().Get("mode") {
		case "missing":
			return nil, model.NotFound("task %s not found", "t1")
		case "details":
			return nil, &model.Error{Code: "conflict", Message: "stale", Details: map[string]any{"version": 3}}
		case "raw":
			return nil, errors.New("db exploded")
		}
		return map[string]any{"id": "t1"}, nil
	})

	ok := do(t, h, http.MethodGet, "/", "")
	if ok.status != http.StatusCreated || ok.ctype != "application/json" || ok.body["id"] != "t1" {
		t.Fatalf("ok = %+v", ok)
	}

	nf := do(t, h, http.MethodGet, "/?mode=missing", "")
	e := errorOf(t, nf)
	if nf.status != http.StatusNotFound || nf.ctype != "application/json" || e["code"] != "not_found" || e["message"] != "task t1 not found" {
		t.Fatalf("not found = %+v", nf)
	}
	if _, present := e["details"]; present {
		t.Fatalf("empty details serialized: %v", e)
	}

	cf := do(t, h, http.MethodGet, "/?mode=details", "")
	e = errorOf(t, cf)
	d, _ := e["details"].(map[string]any)
	if cf.status != http.StatusConflict || e["code"] != "conflict" || e["message"] != "stale" || d["version"] != float64(3) {
		t.Fatalf("conflict = %+v", cf)
	}

	in := do(t, h, http.MethodGet, "/?mode=raw", "")
	e = errorOf(t, in)
	if in.status != http.StatusInternalServerError || e["code"] != "internal" || e["message"] != "internal error" {
		t.Fatalf("internal = %+v", in)
	}
}

func TestDecodeBodyRules(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := struct {
			Name string `json:"name"`
		}{Name: "unset"}
		if err := Decode(w, r, 16, &v); err != nil {
			WriteError(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"name": v.Name})
	})

	if c := do(t, h, http.MethodPost, "/", `{"name":"ada"}`); c.status != http.StatusOK || c.body["name"] != "ada" {
		t.Fatalf("valid = %+v", c)
	}
	if c := do(t, h, http.MethodPost, "/", ""); c.status != http.StatusOK || c.body["name"] != "unset" {
		t.Fatalf("empty = %+v", c)
	}
	c := do(t, h, http.MethodPost, "/", `{"name":"`+strings.Repeat("x", 32)+`"}`)
	if e := errorOf(t, c); c.status != http.StatusBadRequest || e["message"] != "request body too large or unreadable" {
		t.Fatalf("too large = %+v", c)
	}
	c = do(t, h, http.MethodPost, "/", `{"name":`)
	if e := errorOf(t, c); c.status != http.StatusBadRequest || e["message"] != "invalid JSON body" {
		t.Fatalf("invalid = %+v", c)
	}
}

func TestQueryLimits(t *testing.T) {
	h := Handle(http.StatusOK, func(r *http.Request) (any, error) {
		n, err := PositiveLimit(r, 50, 200)
		if err != nil {
			return nil, err
		}
		return map[string]any{"positive": n, "limit": Limit(r), "since": strconv.FormatInt(Int64(r, "since"), 10)}, nil
	})

	for _, c := range []struct {
		query          string
		positive, list float64
		since          string
	}{
		{"", 50, 0, "0"},
		{"?limit=1", 1, 1, "0"},
		{"?limit=200", 200, 200, "0"},
		{"?limit=201", 200, 201, "0"},
		{"?limit=5000", 200, 1000, "0"},
		{"?limit=7&since=9223372036854775807", 7, 7, "9223372036854775807"},
	} {
		got := do(t, h, http.MethodGet, "/"+c.query, "")
		if got.status != http.StatusOK || got.body["positive"] != c.positive || got.body["limit"] != c.list || got.body["since"] != c.since {
			t.Errorf("%q = %+v", c.query, got)
		}
	}
	for _, q := range []string{"0", "-1", "abc", "1.5"} {
		got := do(t, h, http.MethodGet, "/?limit="+q, "")
		if e := errorOf(t, got); got.status != http.StatusBadRequest || e["message"] != "limit must be a positive integer" {
			t.Errorf("limit=%s = %+v", q, got)
		}
	}
}

func TestPageCursor(t *testing.T) {
	h := Handle(http.StatusOK, func(r *http.Request) (any, error) {
		return Page("tasks", []string{"a"}, r.URL.Query().Get("next")), nil
	})
	last := do(t, h, http.MethodGet, "/", "")
	if v, present := last.body["next_cursor"]; !present || v != nil {
		t.Fatalf("last page = %+v", last)
	}
	more := do(t, h, http.MethodGet, "/?next=c2", "")
	if more.body["next_cursor"] != "c2" || len(more.body["tasks"].([]any)) != 1 {
		t.Fatalf("next page = %+v", more)
	}
}

func TestFailLogsOnlyInternalErrors(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("mode") == "raw" {
			Fail(w, log, "load task", errors.New("db exploded"))
			return
		}
		Fail(w, log, "load task", model.Forbidden("nope"))
	})

	c := do(t, h, http.MethodGet, "/", "")
	if c.status != http.StatusForbidden || logs.Len() != 0 {
		t.Fatalf("forbidden = %+v logs=%q", c, logs.String())
	}
	c = do(t, h, http.MethodGet, "/?mode=raw", "")
	if c.status != http.StatusInternalServerError || !strings.Contains(logs.String(), "load task") || !strings.Contains(logs.String(), "db exploded") {
		t.Fatalf("internal = %+v logs=%q", c, logs.String())
	}
	if c := do(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		Fail(w, nil, "x", errors.New("y"))
	}), http.MethodGet, "/", ""); c.status != http.StatusInternalServerError {
		t.Fatalf("nil logger = %+v", c)
	}
}
