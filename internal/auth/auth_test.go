package auth

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestNewTokenFormat(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("tokens repeat")
	}
	for _, tok := range []string{a, b} {
		if !strings.HasPrefix(tok, TokenPrefix) {
			t.Fatalf("token %q lacks prefix", tok)
		}
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, TokenPrefix))
		if err != nil || len(raw) != 32 {
			t.Fatalf("token payload %d bytes, err %v", len(raw), err)
		}
	}
}

func TestOwnerMatch(t *testing.T) {
	cases := []struct {
		presented, owner string
		want             bool
	}{
		{"owner-token-0123456789ab", "owner-token-0123456789ab", true},
		{"owner-token-0123456789ac", "owner-token-0123456789ab", false},
		{"", "owner-token-0123456789ab", false},
		{"", "", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := OwnerMatch(c.presented, c.owner); got != c.want {
			t.Errorf("OwnerMatch(%q, %q) = %v", c.presented, c.owner, got)
		}
	}
}

func TestAuditNote(t *testing.T) {
	ctx := context.Background()
	if got := AuditNote(ctx, "n"); got != "n" {
		t.Fatalf("without principal: %q", got)
	}
	if got := AuditNote(WithPrincipal(ctx, Principal{Scope: ScopeOwner}), "n"); got != "n" {
		t.Fatalf("empty token id: %q", got)
	}
	k := WithPrincipal(ctx, Principal{TokenID: "K-1", Scope: ScopeOwner})
	if got := AuditNote(k, ""); got != "token=K-1" {
		t.Fatalf("no note: %q", got)
	}
	if got := AuditNote(k, "merged"); got != "token=K-1; merged" {
		t.Fatalf("with note: %q", got)
	}
}
