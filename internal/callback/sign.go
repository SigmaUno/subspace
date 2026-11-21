package callback

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderEvent     = "X-Subspace-Event"
	HeaderEventID   = "X-Subspace-Event-Id"
	HeaderTimestamp = "X-Subspace-Timestamp"
	HeaderSignature = "X-Subspace-Signature"

	secretPrefix = "sscb_"
	sigPrefix    = "sha256="
)

var (
	ErrBadSignature = errors.New("callback: signature mismatch")
	ErrStale        = errors.New("callback: timestamp outside tolerance")
)

func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return secretPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func mac(secret string, ts int64, body []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(ts, 10)))
	m.Write([]byte{'.'})
	m.Write(body)
	return m.Sum(nil)
}

func Sign(secret string, ts int64, body []byte) string {
	return sigPrefix + hex.EncodeToString(mac(secret, ts, body))
}

func Verify(secret string, ts int64, body []byte, signature string, now time.Time, tolerance time.Duration) error {
	got, err := hex.DecodeString(strings.TrimPrefix(signature, sigPrefix))
	if err != nil || !hmac.Equal(got, mac(secret, ts, body)) {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return ErrStale
	}
	return nil
}
