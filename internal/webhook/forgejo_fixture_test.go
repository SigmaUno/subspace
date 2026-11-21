package webhook

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestRecordedForgejoDeliveries(t *testing.T) {
	const url = "https://forge.test/subspace-admin/demo/pulls/1"
	tests := []struct {
		name string
		want Event
	}{
		{"forgejo_live_synchronized", Event{ProviderForgejo, "subspace-admin/demo", 1, "feat-c", false, false, url}},
		{"forgejo_live_merged", Event{ProviderForgejo, "subspace-admin/demo", 1, "feat-c", true, false, url}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := fixture(t, tc.name+".body.json")
			raw, err := os.ReadFile(filepath.Join("testdata", tc.name+".headers.json"))
			if err != nil {
				t.Fatal(err)
			}
			hdr := map[string]string{}
			if err := json.Unmarshal(raw, &hdr); err != nil {
				t.Fatal(err)
			}
			sig := sign(testSecret, body)
			for _, name := range []string{"X-Forgejo-Signature", "X-Gitea-Signature", "X-Gogs-Signature"} {
				hdr[name] = sig
			}
			hdr["X-Hub-Signature-256"] = "sha256=" + sig

			rec := &recorder{}
			h, err := NewProviderHandler(ProviderForgejo, testSecret, rec.fn)
			if err != nil {
				t.Fatal(err)
			}
			if w := post(h, body, hdr); w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body)
			}
			if len(rec.events) != 1 || rec.events[0] != tc.want {
				t.Fatalf("events = %+v, want %+v", rec.events, tc.want)
			}

			gh, err := NewProviderHandler(ProviderGitHub, testSecret, (&recorder{}).fn)
			if err != nil {
				t.Fatal(err)
			}
			if w := post(gh, body, hdr); w.Code != http.StatusUnauthorized {
				t.Fatalf("github handler accepted a forgejo delivery: %d", w.Code)
			}
		})
	}
}
