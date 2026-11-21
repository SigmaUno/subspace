package actionlink

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	DefaultTTL = 30 * time.Minute
	MaxTTL     = 24 * time.Hour
	maxToken   = 1024
	nonceBytes = 16
)

var (
	ErrInvalid = errors.New("actionlink: invalid token")
	ErrExpired = errors.New("actionlink: token expired")
)

var b64 = base64.RawURLEncoding.Strict()

type Claims struct {
	Decision string `json:"d"`
	Action   string `json:"a"`
	Option   int    `json:"o"`
	Nonce    string `json:"n"`
	Expires  int64  `json:"e"`
}

func (c Claims) ExpiresAt() time.Time { return time.Unix(c.Expires, 0).UTC() }

type Issuer struct {
	sign []byte
	csrf []byte
	now  func() time.Time
}

func NewIssuer(secret []byte, now func() time.Time) (*Issuer, error) {
	if len(secret) < 24 {
		return nil, errors.New("actionlink: secret must be at least 24 bytes")
	}
	if now == nil {
		now = time.Now
	}
	sign, err := hkdf.Key(sha256.New, secret, []byte("subspace"), "action-link sign v1", 32)
	if err != nil {
		return nil, err
	}
	csrf, err := hkdf.Key(sha256.New, secret, []byte("subspace"), "action-link csrf v1", 32)
	if err != nil {
		return nil, err
	}
	return &Issuer{sign: sign, csrf: csrf, now: now}, nil
}

func (i *Issuer) Issue(decision, action string, option int, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if decision == "" || action == "" || option < 0 || ttl > MaxTTL {
		return "", time.Time{}, ErrInvalid
	}
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", time.Time{}, err
	}
	exp := i.now().Add(ttl).Truncate(time.Second).UTC()
	payload, err := json.Marshal(Claims{Decision: decision, Action: action, Option: option, Nonce: b64.EncodeToString(nonce), Expires: exp.Unix()})
	if err != nil {
		return "", time.Time{}, err
	}
	body := b64.EncodeToString(payload)
	return body + "." + b64.EncodeToString(mac(i.sign, "token", body)), exp, nil
}

func (i *Issuer) Verify(token string) (Claims, error) {
	if len(token) > maxToken {
		return Claims{}, ErrInvalid
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, ErrInvalid
	}
	got, err := b64.DecodeString(sig)
	if err != nil || !hmac.Equal(got, mac(i.sign, "token", body)) {
		return Claims{}, ErrInvalid
	}
	raw, err := b64.DecodeString(body)
	if err != nil {
		return Claims{}, ErrInvalid
	}
	var c Claims
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil || c.Decision == "" || c.Action == "" || c.Nonce == "" || c.Option < 0 {
		return Claims{}, ErrInvalid
	}
	if !i.now().Before(c.ExpiresAt()) {
		return Claims{}, ErrExpired
	}
	return c, nil
}

func (i *Issuer) CSRF(c Claims) (cookie, field string, err error) {
	r := make([]byte, nonceBytes)
	if _, err := rand.Read(r); err != nil {
		return "", "", err
	}
	cookie = b64.EncodeToString(r)
	return cookie, cookie + "." + b64.EncodeToString(mac(i.csrf, c.Nonce, cookie)), nil
}

func (i *Issuer) CheckCSRF(c Claims, cookie, field string) bool {
	render, sig, ok := strings.Cut(field, ".")
	if !ok || cookie == "" {
		return false
	}
	got, err := b64.DecodeString(sig)
	if err != nil {
		return false
	}
	sameRender := hmac.Equal([]byte(render), []byte(cookie))
	return hmac.Equal(got, mac(i.csrf, c.Nonce, render)) && sameRender
}

func mac(key []byte, domain, msg string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(domain))
	m.Write([]byte{0})
	m.Write([]byte(msg))
	return m.Sum(nil)
}
