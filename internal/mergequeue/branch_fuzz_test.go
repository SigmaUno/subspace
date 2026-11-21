package mergequeue

import (
	"os/exec"
	"strings"
	"testing"
)

func FuzzValidBranch(f *testing.F) {
	for _, s := range []string{
		"main", "sub/t-3-add-login", "feature/x.y", "-rf", "--upload-pack=x", "/abs", "a//b", "a/", "a..b", "a.lock", "a@{b", "a b",
		"a\tb", "a~1", "a^", "a:b", "a?", "a*", "a[", `a\b`, "", ".", "a/.hidden", ".hidden", "a.lock/b", "@", "a/@", "é🙂", "a\x00b", "a\x7f", "HEAD", "refs/heads/x",
	} {
		f.Add(s)
	}
	_, gitErr := exec.LookPath("git")
	f.Fuzz(func(t *testing.T, b string) {
		if !validBranch(b) {
			return
		}
		if b == "" || strings.HasPrefix(b, "-") || strings.ContainsAny(b, " \t\r\n\x00\x7f~^:?*[\\") {
			t.Fatalf("validBranch accepted %q", b)
		}
		if gitErr != nil {
			return
		}
		if err := exec.Command("git", "check-ref-format", "refs/heads/"+b).Run(); err != nil {
			t.Fatalf("validBranch accepted %q but git rejects it", b)
		}
	})
}
