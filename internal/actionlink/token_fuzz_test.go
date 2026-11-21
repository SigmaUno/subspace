package actionlink

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var fuzzNow = time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)

func fuzzIssuer(t testing.TB) *Issuer {
	i, err := NewIssuer([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return fuzzNow })
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func FuzzVerify(f *testing.F) {
	i := fuzzIssuer(f)
	tok, _, err := i.Issue("q:Q-1", "answer", 2, time.Hour)
	if err != nil {
		f.Fatal(err)
	}
	expired, _, err := i.Issue("plan:P-1", "approve", 0, time.Minute)
	if err != nil {
		f.Fatal(err)
	}
	body, sig, _ := strings.Cut(tok, ".")
	for _, s := range []string{tok, expired, "", ".", "..", "a.b", body, body + ".", "." + sig, tok + tok, tok + ".x", strings.Repeat("A", 2000), tok[:len(tok)-1], "e30.e30", "\xff.\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, token string) {
		i := fuzzIssuer(t)
		c, err := i.Verify(token)
		if err != nil {
			if !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrExpired) {
				t.Fatalf("unexpected error %v", err)
			}
			if _, _, cerr := i.CSRF(Claims{Nonce: token}); cerr != nil {
				t.Fatal(cerr)
			}
			return
		}
		if c.Decision == "" || c.Action == "" || c.Nonce == "" || c.Option < 0 || !c.ExpiresAt().After(fuzzNow) || len(token) > maxToken {
			t.Fatalf("accepted bad claims %+v", c)
		}
		if i.CheckCSRF(c, token, token) {
			t.Fatal("CSRF accepted a forged field")
		}
	})
}

func FuzzIssueVerifyRoundTrip(f *testing.F) {
	f.Add("q:Q-1", "answer", 0, int64(60))
	f.Add("plan:P-1", "reject", 3, int64(0))
	f.Add("a.b", "c.d", 1, int64(86400))
	f.Add("é🙂", "\"quoted\"", 7, int64(1))
	f.Add("x", "y", -1, int64(5))
	f.Add("x", "y", 0, int64(86401))
	f.Add("", "y", 0, int64(5))
	f.Fuzz(func(t *testing.T, decision, action string, option int, ttlSeconds int64) {
		if !utf8.ValidString(decision) || !utf8.ValidString(action) || ttlSeconds < -1 || ttlSeconds > 1e9 {
			return
		}
		i := fuzzIssuer(t)
		ttl := time.Duration(ttlSeconds) * time.Second
		tok, exp, err := i.Issue(decision, action, option, ttl)
		valid := decision != "" && action != "" && option >= 0 && ttl <= MaxTTL
		if (err == nil) != valid {
			t.Fatalf("Issue(%q, %q, %d, %s) err = %v, valid = %v", decision, action, option, ttl, err, valid)
		}
		if err != nil {
			return
		}
		if len(tok) > maxToken {
			if _, err := i.Verify(tok); !errors.Is(err, ErrInvalid) {
				t.Fatalf("oversized token verified: %v", err)
			}
			return
		}
		c, err := i.Verify(tok)
		if err != nil {
			t.Fatalf("Verify(Issue(...)) = %v", err)
		}
		if c.Decision != decision || c.Action != action || c.Option != option || !c.ExpiresAt().Equal(exp) {
			t.Fatalf("claims %+v do not match issue %q %q %d %s", c, decision, action, option, exp)
		}
		cookie, field, err := i.CSRF(c)
		if err != nil || !i.CheckCSRF(c, cookie, field) {
			t.Fatalf("CSRF round trip failed: %v", err)
		}
		for k := range tok {
			mut := []byte(tok)
			mut[k] ^= 1
			if _, err := i.Verify(string(mut)); err == nil {
				t.Fatalf("tampered token verified: %q", mut)
			}
		}
	})
}

func TestVerifyRejectsNonCanonicalSignature(t *testing.T) {
	i := fuzzIssuer(t)
	tok, _, err := i.Issue("q:Q-1", "answer", 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	last := tok[len(tok)-1]
	n := strings.IndexByte(alphabet, last)
	other := alphabet[n^1]
	if _, err := i.Verify(tok[:len(tok)-1] + string(other)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("token with altered padding bits verified: %v", err)
	}
}
