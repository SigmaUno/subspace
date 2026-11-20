package forgejo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SigmaUno/subspace/internal/githost"
)

func TestGetPR(t *testing.T) {
	tests := []struct {
		name    string
		repo    string
		number  int64
		status  int
		body    string
		want    githost.PR
		wantErr error
		anyErr  bool
	}{
		{
			name: "open", repo: "o/r", number: 5, status: 200,
			body: `{"number":5,"title":"T","state":"open","merged":false,"html_url":"https://f/o/r/pulls/5","head":{"ref":"sub/t-1-x"},"base":{"ref":"main"}}`,
			want: githost.PR{Repo: "o/r", Number: 5, Title: "T", State: "open", HeadBranch: "sub/t-1-x", BaseBranch: "main", URL: "https://f/o/r/pulls/5"},
		},
		{
			name: "merged", repo: "o/r", number: 5, status: 200,
			body: `{"number":5,"title":"T","state":"closed","merged":true,"html_url":"u","head":{"ref":"h"},"base":{"ref":"main"}}`,
			want: githost.PR{Repo: "o/r", Number: 5, Title: "T", State: "closed", Merged: true, HeadBranch: "h", BaseBranch: "main", URL: "u"},
		},
		{name: "not found", repo: "o/r", number: 5, status: 404, body: `{}`, wantErr: githost.ErrNotFound},
		{name: "server error", repo: "o/r", number: 5, status: 500, body: `oops`, anyErr: true},
		{name: "bad json", repo: "o/r", number: 5, status: 200, body: `{`, anyErr: true},
		{name: "bad repo", repo: "nope", number: 5, anyErr: true},
		{name: "bad number", repo: "o/r", number: 0, anyErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(srv.URL+"/", "tok", srv.Client())
			pr, err := c.GetPR(context.Background(), tc.repo, tc.number)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v got %v", tc.wantErr, err)
				}
				return
			}
			if tc.anyErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pr != tc.want {
				t.Fatalf("got %+v want %+v", pr, tc.want)
			}
			if gotPath != "/api/v1/repos/o/r/pulls/5" {
				t.Errorf("path %q", gotPath)
			}
			if gotAuth != "token tok" {
				t.Errorf("auth %q", gotAuth)
			}
		})
	}
}

func TestGetPRContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(srv.URL, "t", nil).GetPR(ctx, "o/r", 1); err == nil {
		t.Fatal("expected error")
	}
}

func TestHostDelegation(t *testing.T) {
	var h githost.Host = New("http://x", "", nil)
	if repo, n, err := h.ParsePRURL("http://x/o/r/pulls/9"); err != nil || repo != "o/r" || n != 9 {
		t.Fatalf("%q %d %v", repo, n, err)
	}
	if err := h.ValidateBranch("T-1", "a", "sub/t-1-a"); err != nil {
		t.Fatal(err)
	}
	if h.VerifyWebhook("s", []byte("b"), "00") {
		t.Fatal("should not verify")
	}
}

func TestParsePullRequestEvent(t *testing.T) {
	merged := `{"action":"closed","number":8,"pull_request":{"number":8,"title":"Add","state":"closed","merged":true,"html_url":"https://f/o/r/pulls/8","head":{"ref":"sub/t-8-add"},"base":{"ref":"main"}},"repository":{"full_name":"o/r"},"sender":{"login":"owner"}}`
	opened := `{"action":"opened","pull_request":{"number":9,"state":"open","head":{"ref":"h"},"base":{"ref":"main"}},"repository":{"full_name":"o/r"}}`
	tests := []struct {
		name, event, body string
		want              githost.PullRequestEvent
		merged            bool
		wantErr           bool
	}{
		{"merged", "pull_request", merged, githost.PullRequestEvent{Action: "closed", Repo: "o/r", Number: 8, Title: "Add", State: "closed", Merged: true, HeadBranch: "sub/t-8-add", BaseBranch: "main", URL: "https://f/o/r/pulls/8", Sender: "owner"}, true, false},
		{"opened number fallback", "pull_request", opened, githost.PullRequestEvent{Action: "opened", Repo: "o/r", Number: 9, State: "open", HeadBranch: "h", BaseBranch: "main"}, false, false},
		{"other event", "push", merged, githost.PullRequestEvent{}, false, true},
		{"bad json", "pull_request", `{`, githost.PullRequestEvent{}, false, true},
		{"missing pr", "pull_request", `{"action":"closed","repository":{"full_name":"o/r"}}`, githost.PullRequestEvent{}, false, true},
		{"missing repo", "pull_request", `{"action":"closed","pull_request":{"number":1}}`, githost.PullRequestEvent{}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := ParsePullRequestEvent(tc.event, []byte(tc.body))
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if ev != tc.want {
				t.Fatalf("got %+v want %+v", ev, tc.want)
			}
			if ev.IsMerged() != tc.merged {
				t.Errorf("IsMerged = %v", ev.IsMerged())
			}
		})
	}
	if _, err := ParsePullRequestEvent("push", nil); !errors.Is(err, ErrNotPullRequest) {
		t.Errorf("want ErrNotPullRequest, got %v", err)
	}
}
