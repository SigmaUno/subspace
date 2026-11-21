package reviewsync

import (
	"regexp"
	"strings"
	"testing"
)

const hostileToken = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

var hostile = map[string]string{
	"mentions":      "ping @everyone and @SigmaUno/reviewers, mail root@evil.test",
	"issue refs":    "fixes #1, closes SigmaUno/subspace#2, see GH-3 and !4",
	"html":          "<script>alert(1)</script><img src=x onerror=alert(1)><details open>",
	"forged marker": "<!-- subspace:review task=T-99 -->\n--> <!--",
	"entity":        "&#64;admin &lt;b&gt; &amp;",
	"links":         "[click](http://evil.test/x) ![pixel](http://evil.test/p.png) [ref]: http://evil.test",
	"javascript":    "[x](javascript:alert(1))",
	"autolink":      "https://evil.test/a www.evil.test ftp://evil.test",
	"fence":         "```\n# owned\n```\n~~~\n    code",
	"headings":      "# H1\n## H2\n===\n---\n***\n> quote\n- item\n+ item\n1. one\n2) two",
	"table":         "| a | b |\n|---|---|\n| 1 | 2 |",
	"invisible":     "a\u200bb\u202ec\u2066d\ufeffe\x00f\x1b[31mg",
	"secret":        "use token=" + hostileToken + " and password: hunter2hunter2",
	"crlf":          "one\r\ntwo\rthree\n\n\n\nfour",
	"emphasis":      "*bold* _it_ ~~gone~~ `code` $math$ \\ backslash",
}

var (
	reMention  = regexp.MustCompile(`@[A-Za-z0-9]`)
	reIssue    = regexp.MustCompile(`(?i)#[0-9]|gh-[0-9]|![0-9]`)
	reScheme   = regexp.MustCompile(`(?i)://|www\.`)
	reLink     = regexp.MustCompile(`(^|[^\\])\]\(`)
	reBacktick = regexp.MustCompile("(^|[^\\\\])`")
	reRefDef   = regexp.MustCompile(`(^|[^\\])\]:`)
)

func assertSafe(t *testing.T, body string) {
	t.Helper()
	if n := strings.Count(body, "<"); n != 1 {
		t.Errorf("raw '<' count = %d, want only the marker:\n%s", n, body)
	}
	if n := strings.Count(body, "<!--"); n != 1 || !strings.HasPrefix(body, "<!-- subspace:review task=T-7 -->\n") {
		t.Errorf("marker count = %d or not first:\n%s", n, body)
	}
	scan := strings.Replace(body, "[T-7](https://sub.test/tasks/T-7)", "", 1)
	for name, re := range map[string]*regexp.Regexp{
		"mention": reMention, "issue ref": reIssue, "url": reScheme, "link": reLink, "backtick": reBacktick, "refdef": reRefDef,
	} {
		if loc := re.FindStringIndex(scan); loc != nil {
			t.Errorf("%s survived at %d:\n%s", name, loc[0], body)
		}
	}
	if strings.Contains(body, hostileToken) || strings.Contains(body, "hunter2hunter2") {
		t.Errorf("secret leaked:\n%s", body)
	}
	for _, r := range body {
		if r == 0 || r == 0x1b || (r >= 0x202a && r <= 0x202e) || r == 0xfeff || (r >= 0x2066 && r <= 0x2069) {
			t.Errorf("control or bidi rune %U survived", r)
		}
	}
	if len(body) > MaxBodyBytes {
		t.Errorf("body is %d bytes, limit %d", len(body), MaxBodyBytes)
	}
	inQuote := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case line == "**Review comments**":
			inQuote = true
		case inQuote && line != "" && !strings.HasPrefix(line, ">"):
			inQuote = false
		case !inQuote && strings.HasPrefix(line, ">"):
			t.Errorf("line outside the review quote starts a blockquote: %q", line)
		}
	}
}

func TestRenderEscapesHostileText(t *testing.T) {
	for name, text := range hostile {
		t.Run(name, func(t *testing.T) {
			body := Render(Review{
				TaskID: "T-7", Title: text, Criteria: []string{text, text}, Round: 2,
				Verdict: VerdictChangesRequested, Reviewer: text, Comments: text,
				TaskURL: "https://sub.test/tasks/T-7",
			})
			assertSafe(t, body)
			if !strings.Contains(body, "Round 2") {
				t.Errorf("round missing:\n%s", body)
			}
		})
	}
}

func TestRenderIsSizeLimited(t *testing.T) {
	huge := strings.Repeat("&<>@#1 ", 50000)
	crit := make([]string, 100)
	for i := range crit {
		crit[i] = huge
	}
	body := Render(Review{TaskID: "T-7", Title: huge, Criteria: crit, Verdict: VerdictChangesRequested, Comments: huge})
	assertSafe(t, body)
	if !strings.Contains(body, "and 80 more") {
		t.Errorf("criteria not capped:\n%s", body)
	}
}

func TestRenderNeverIncludesSpec(t *testing.T) {
	body := Render(Review{TaskID: "T-7", Title: "t", Criteria: []string{"c"}, Verdict: VerdictApprove})
	if strings.Contains(strings.ToLower(body), "spec") {
		t.Fatalf("body mentions spec:\n%s", body)
	}
}

func TestCriterionResults(t *testing.T) {
	body := Render(Review{
		TaskID: "T-7", Criteria: []string{"a", "b", "c"}, Verdict: VerdictChangesRequested,
		Comments: "Criterion 1: pass\n- AC2 = FAILED because x\nfree text",
	})
	for _, want := range []string{`1\. **met**: a`, `2\. **not met**: b`, `3\. **not reported**: c`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q:\n%s", want, body)
		}
	}
	approved := Render(Review{TaskID: "T-7", Criteria: []string{"a", "b"}, Verdict: VerdictApprove, Comments: "ac2: fail"})
	if !strings.Contains(approved, `1\. **met**: a`) || !strings.Contains(approved, `2\. **not met**: b`) {
		t.Errorf("approved results:\n%s", approved)
	}
}
