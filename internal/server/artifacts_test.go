package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var pngHeader = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00")

func (e *env) upload(token, query, name, ctype string, content []byte) (int, map[string]any) {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="`+name+`"`)
	if ctype != "" {
		hdr.Set("Content-Type", ctype)
	}
	part, _ := mw.CreatePart(hdr)
	_, _ = part.Write(content)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/artifacts?"+query, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func (e *env) download(id string) (*http.Response, []byte) {
	e.t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/v1/artifacts/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+ownerTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestArtifactsOwnerRESTLifecycle(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SUBSPACE_DATA_DIR", dataDir)
	t.Setenv("SUBSPACE_ARTIFACT_MAX_FILE_BYTES", "4096")
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	wk := e.register("w1", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})

	code, a := e.upload("", "scope=agent:w1", "x.png", "", pngHeader)
	if code != 401 {
		t.Fatalf("no token: %d %v", code, a)
	}
	if code, a = e.upload(wk, "scope=agent:w1", "x.png", "", pngHeader); code != 403 {
		t.Fatalf("agent token on owner REST: %d %v", code, a)
	}

	code, a = e.upload(ownerTok, "scope=agent:w1", "shot.png", "", pngHeader)
	if code != 201 || a["content_type"] != "image/png" || a["uploaded_by"] != "owner" {
		t.Fatalf("sniffed upload: %d %v", code, a)
	}
	pngID := a["id"].(string)

	code, a = e.upload(ownerTok, "scope=task:"+ids[0], "../../../etc/passwd", "text/plain", []byte("hello"))
	if code != 201 || a["name"] != "passwd" {
		t.Fatalf("traversal name: %d %v", code, a)
	}
	taskArt := a["id"].(string)
	if code, a = e.upload(ownerTok, "scope=project:"+bid, "notes.txt", "text/plain", []byte("brief notes")); code != 201 {
		t.Fatalf("project upload: %d %v", code, a)
	}

	for _, bad := range []string{"scope=agent:../x", "scope=task:T-999", "scope=agent:nobody", "scope=project:B-999", ""} {
		if code, a = e.upload(ownerTok, bad, "f.txt", "text/plain", []byte("x")); code == 201 {
			t.Fatalf("scope %q accepted: %v", bad, a)
		}
	}
	if code, a = e.upload(ownerTok, "scope=agent:w1", "big.bin", "application/octet-stream", make([]byte, 8192)); code != 413 {
		t.Fatalf("oversize: %d %v", code, a)
	}

	resp, body := e.download(pngID)
	if resp.StatusCode != 200 || !bytes.Equal(body, pngHeader) || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %v %q", resp.StatusCode, resp.Header, body)
	}

	out := e.restWant(200, "GET", "/api/v1/artifacts?scope=task:"+ids[0], nil)
	if l := out["artifacts"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != taskArt {
		t.Fatalf("scoped list: %v", out)
	}
	if l := e.restWant(200, "GET", "/api/v1/artifacts", nil)["artifacts"].([]any); len(l) != 3 {
		t.Fatalf("list: %v", l)
	}

	e.restWant(204, "DELETE", "/api/v1/artifacts/"+taskArt, nil)
	e.restWant(404, "GET", "/api/v1/artifacts/"+taskArt, nil)
	if _, out := e.rest("GET", "/api/v1/audit?task_id="+ids[0], nil); !strings.Contains(toJSON(out), "delete_artifact") || !strings.Contains(toJSON(out), "put_artifact") {
		t.Fatalf("audit: %v", out)
	}

	err := filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "passwd" {
			t.Errorf("client-supplied name used on disk: %s", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestArtifactsMCPToolsAreScopedToAgent(t *testing.T) {
	t.Setenv("SUBSPACE_DATA_DIR", t.TempDir())
	e := newEnv(t)
	lead := e.register("rex", "leader", "other")
	w1 := e.register("w1", "worker", "claude-code")
	w2 := e.register("w2", "worker", "claude-code")
	bid := e.brief(map[string]any{})
	ids := e.approvedPlan(lead, bid, planTask("a", nil), planTask("b", nil))
	e.must(lead, "assign", map[string]any{"task_id": ids[0], "agent_id": "w1"})
	e.must(lead, "assign", map[string]any{"task_id": ids[1], "agent_id": "w2"})

	content := []byte("log line\n")
	put := e.must(w1, "put_artifact", map[string]any{"scope": "task:" + ids[0], "name": "run.log", "content_base64": base64.StdEncoding.EncodeToString(content)})
	art := put["artifact"].(map[string]any)
	if art["content_type"] != "text/plain; charset=utf-8" || art["uploaded_by"] != "w1" {
		t.Fatalf("put: %v", art)
	}
	id := art["id"].(string)

	got := e.must(w1, "get_artifact", map[string]any{"id": id})
	if raw, _ := base64.StdEncoding.DecodeString(got["content_base64"].(string)); !bytes.Equal(raw, content) {
		t.Fatalf("get: %v", got)
	}
	if l := e.must(w1, "list_artifacts", nil)["artifacts"].([]any); len(l) != 1 {
		t.Fatalf("list: %v", l)
	}
	if l := e.must(w2, "list_artifacts", nil)["artifacts"].([]any); len(l) != 0 {
		t.Fatalf("w2 sees foreign artifacts: %v", l)
	}
	e.mustFail(w2, "get_artifact", map[string]any{"id": id}, "forbidden")
	e.mustFail(w2, "put_artifact", map[string]any{"scope": "task:" + ids[0], "name": "x", "content_base64": "eA=="}, "forbidden")
	e.mustFail(w2, "put_artifact", map[string]any{"scope": "agent:w1", "name": "x", "content_base64": "eA=="}, "forbidden")
	e.must(lead, "get_artifact", map[string]any{"id": id})

	if out := e.restWant(200, "GET", "/api/v1/artifacts?scope=task:"+ids[0], nil); len(out["artifacts"].([]any)) != 1 {
		t.Fatalf("owner list: %v", out)
	}
}

func TestArtifactsProjectQuotaEnforced(t *testing.T) {
	t.Setenv("SUBSPACE_DATA_DIR", t.TempDir())
	t.Setenv("SUBSPACE_ARTIFACT_PROJECT_QUOTA_BYTES", "100")
	e := newEnv(t)
	bid := e.brief(map[string]any{})
	if code, a := e.upload(ownerTok, "scope=project:"+bid, "a.bin", "application/octet-stream", make([]byte, 80)); code != 201 {
		t.Fatalf("first: %d %v", code, a)
	}
	if code, a := e.upload(ownerTok, "scope=project:"+bid, "b.bin", "application/octet-stream", bytes.Repeat([]byte{1}, 40)); code != 507 {
		t.Fatalf("over quota: %d %v", code, a)
	}
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
