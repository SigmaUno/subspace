package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	recordSize = 4096
	saltLen    = 16
	authLen    = 16
	keyLen     = 65
	headerLen  = saltLen + 4 + 1 + keyLen
	tagLen     = 16
	MaxPayload = recordSize - headerLen - tagLen - 1
	jwtTTL     = 12 * time.Hour
	jwtReuse   = time.Hour
)

var b64 = base64.RawURLEncoding

var ErrTooLarge = errors.New("webpush: payload too large")

type Keys struct {
	P256DH []byte
	Auth   []byte
}

func ParseKeys(p256dh, auth string) (Keys, error) {
	pub, err := decode64(p256dh)
	if err != nil || len(pub) != keyLen {
		return Keys{}, errors.New("keys.p256dh must be a base64url P-256 public key")
	}
	if _, err := ecdh.P256().NewPublicKey(pub); err != nil {
		return Keys{}, errors.New("keys.p256dh is not a valid P-256 point")
	}
	sec, err := decode64(auth)
	if err != nil || len(sec) != authLen {
		return Keys{}, errors.New("keys.auth must be 16 bytes of base64url")
	}
	return Keys{P256DH: pub, Auth: sec}, nil
}

func decode64(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(strings.TrimSpace(s), "="))
}

func Encrypt(plaintext []byte, k Keys, pad int) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return seal(plaintext, k, as, salt, pad)
}

func seal(plaintext []byte, k Keys, as *ecdh.PrivateKey, salt []byte, pad int) ([]byte, error) {
	pad = max(pad, 0)
	if len(plaintext)+pad > MaxPayload {
		return nil, ErrTooLarge
	}
	ua, err := ecdh.P256().NewPublicKey(k.P256DH)
	if err != nil {
		return nil, err
	}
	secret, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	prkKey, err := hkdf.Extract(sha256.New, secret, k.Auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(k.P256DH)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	record := make([]byte, len(plaintext)+1+pad)
	copy(record, plaintext)
	record[len(plaintext)] = 0x02
	out := make([]byte, headerLen, headerLen+len(record)+tagLen)
	copy(out, salt)
	binary.BigEndian.PutUint32(out[saltLen:], recordSize)
	out[saltLen+4] = keyLen
	copy(out[saltLen+5:], asPub)
	return gcm.Seal(out, nonce, record, nil), nil
}

type VAPID struct {
	key *ecdsa.PrivateKey
	pub string

	mu    sync.Mutex
	cache map[string]token
}

type token struct {
	jwt string
	exp time.Time
}

func NewVAPID() (*VAPID, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return vapidFrom(k)
}

func ParseVAPID(raw string) (*VAPID, error) {
	b, err := decode64(raw)
	if err != nil {
		return nil, err
	}
	k, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), b)
	if err != nil {
		return nil, err
	}
	return vapidFrom(k)
}

func vapidFrom(k *ecdsa.PrivateKey) (*VAPID, error) {
	pub, err := k.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	return &VAPID{key: k, pub: b64.EncodeToString(pub), cache: map[string]token{}}, nil
}

func (v *VAPID) PublicKey() string { return v.pub }

func (v *VAPID) marshal() (string, error) {
	b, err := v.key.Bytes()
	if err != nil {
		return "", err
	}
	return b64.EncodeToString(b), nil
}

type claims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub,omitempty"`
}

const jwtHeader = `{"typ":"JWT","alg":"ES256"}`

func signingInput(c claims) (string, error) {
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return b64.EncodeToString([]byte(jwtHeader)) + "." + b64.EncodeToString(body), nil
}

func (v *VAPID) sign(c claims) (string, error) {
	input, err := signingInput(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64.EncodeToString(sig), nil
}

func (v *VAPID) Authorization(audience, subject string, now time.Time) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	t, ok := v.cache[audience]
	if !ok || t.exp.Sub(now) < jwtReuse {
		exp := now.Add(jwtTTL).Truncate(time.Second)
		jwt, err := v.sign(claims{Aud: audience, Exp: exp.Unix(), Sub: subject})
		if err != nil {
			return "", fmt.Errorf("webpush: sign vapid token: %w", err)
		}
		if len(v.cache) >= 64 {
			clear(v.cache)
		}
		t = token{jwt: jwt, exp: exp}
		v.cache[audience] = t
	}
	return "vapid t=" + t.jwt + ", k=" + v.pub, nil
}
