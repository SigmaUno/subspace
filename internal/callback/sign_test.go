package callback

import (
	"errors"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"id":"cb_1"}`)
	good := Sign("sscb_secret", now.Unix(), body)
	cases := []struct {
		name   string
		secret string
		ts     int64
		body   []byte
		sig    string
		now    time.Time
		want   error
	}{
		{"valid", "sscb_secret", now.Unix(), body, good, now, nil},
		{"within tolerance", "sscb_secret", now.Unix(), body, good, now.Add(4 * time.Minute), nil},
		{"wrong secret", "sscb_other", now.Unix(), body, good, now, ErrBadSignature},
		{"tampered body", "sscb_secret", now.Unix(), []byte(`{"id":"cb_2"}`), good, now, ErrBadSignature},
		{"tampered timestamp", "sscb_secret", now.Unix() + 1, body, good, now, ErrBadSignature},
		{"not hex", "sscb_secret", now.Unix(), body, "sha256=zz", now, ErrBadSignature},
		{"empty", "sscb_secret", now.Unix(), body, "", now, ErrBadSignature},
		{"stale", "sscb_secret", now.Unix(), body, good, now.Add(6 * time.Minute), ErrStale},
		{"future", "sscb_secret", now.Unix(), body, good, now.Add(-6 * time.Minute), ErrStale},
	}
	for _, c := range cases {
		if err := Verify(c.secret, c.ts, c.body, c.sig, c.now, 5*time.Minute); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
}
