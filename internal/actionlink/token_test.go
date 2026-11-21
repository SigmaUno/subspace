package actionlink

import (
	"errors"
	"testing"
	"time"
)

func TestTokenExpiryOnSimulatedClock(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	iss, err := NewIssuer([]byte("owner-token-for-tests-0123456789"), clock)
	if err != nil {
		t.Fatal(err)
	}
	tok, exp, err := iss.Issue("plan:P-1", "approve", 0, 0)
	if err != nil || !exp.Equal(now.Add(DefaultTTL)) {
		t.Fatalf("issue: %v %v", exp, err)
	}

	now = now.Add(DefaultTTL - time.Second)
	c, err := iss.Verify(tok)
	if err != nil || c.Decision != "plan:P-1" || c.Action != "approve" {
		t.Fatalf("before expiry: %+v %v", c, err)
	}
	now = now.Add(time.Second)
	if _, err := iss.Verify(tok); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expiry: %v", err)
	}

	other, _ := NewIssuer([]byte("another-secret-for-tests-0123456789"), clock)
	now = now.Add(-time.Minute)
	if _, err := other.Verify(tok); !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign secret: %v", err)
	}
	if _, _, err := iss.Issue("plan:P-1", "approve", 0, MaxTTL+time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ttl above max: %v", err)
	}
}
