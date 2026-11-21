package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

const (
	rfcPlain    = "When I grow up, I want to be a watermelon"
	rfcAuth     = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcUAPriv   = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPub    = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcASPriv   = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcSalt     = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcMessage  = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	rfcJWTHead  = "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9"
	rfcJWTBody  = "eyJhdWQiOiJodHRwczovL3B1c2guZXhhbXBsZS5uZXQiLCJleHAiOjE0NTM1MjM3NjgsInN1YiI6Im1haWx0bzpwdXNoQGV4YW1wbGUuY29tIn0"
	rfcJWTSig   = "i3CYb7t4xfxCDquptFOepC9GAu_HLGkMlMuCGSK2rpiUfnK9ojFwDXb1JrErtmysazNjjvW2L9OkSSHzvoD1oA"
	rfcVAPIDKey = "BA1Hxzyi1RUM1b5wjxsn7nGxAszw2u61m164i3MrAIxHF6YK5h4SDYic-dRuU_RCPCfA5aq9ojSwk5Y2EmClBPs"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode64(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decrypt(body []byte, uaPriv *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < headerLen+tagLen {
		return nil, errors.New("short body")
	}
	salt, rs, idlen := body[:saltLen], binary.BigEndian.Uint32(body[saltLen:]), int(body[saltLen+4])
	if idlen != keyLen || rs != recordSize || len(body)-headerLen > int(rs) {
		return nil, errors.New("bad header")
	}
	asPub, err := ecdh.P256().NewPublicKey(body[saltLen+5 : headerLen])
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPub.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[headerLen:], nil)
	if err != nil {
		return nil, err
	}
	i := bytes.LastIndexFunc(plain, func(r rune) bool { return r != 0 })
	if i < 0 || plain[i] != 0x02 {
		return nil, errors.New("bad padding delimiter")
	}
	return plain[:i], nil
}

func TestRFC8291Example(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPriv))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeys(rfcUAPub, rfcAuth)
	if err != nil {
		t.Fatal(err)
	}
	got, err := seal([]byte(rfcPlain), keys, as, mustB64(t, rfcSalt), 0)
	if err != nil {
		t.Fatal(err)
	}
	if b64.EncodeToString(got) != rfcMessage {
		t.Fatalf("message\n got %s\nwant %s", b64.EncodeToString(got), rfcMessage)
	}

	ua, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPriv))
	if err != nil {
		t.Fatal(err)
	}
	for _, pad := range []int{0, 1, 300, MaxPayload - len(rfcPlain)} {
		body, err := Encrypt([]byte(rfcPlain), keys, pad)
		if err != nil {
			t.Fatal(err)
		}
		if len(body) != headerLen+len(rfcPlain)+1+pad+tagLen || len(body) > recordSize {
			t.Fatalf("pad %d: body is %d bytes", pad, len(body))
		}
		plain, err := decrypt(body, ua, keys.Auth)
		if err != nil || string(plain) != rfcPlain {
			t.Fatalf("pad %d: decrypt = %q, %v", pad, plain, err)
		}
	}
	if _, err := Encrypt(make([]byte, MaxPayload), keys, 1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized payload: %v", err)
	}
}

func verifyJWT(t *testing.T, jwt, key string) claims {
	t.Helper()
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), mustB64(t, key))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig := mustB64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature is %d bytes, want raw r||s", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("signature does not verify")
	}
	if string(mustB64(t, parts[0])) != jwtHeader {
		t.Fatalf("header %s", mustB64(t, parts[0]))
	}
	var c claims
	if err := json.Unmarshal(mustB64(t, parts[1]), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRFC8292Example(t *testing.T) {
	input, err := signingInput(claims{Aud: "https://push.example.net", Exp: 1453523768, Sub: "mailto:push@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if input != rfcJWTHead+"."+rfcJWTBody {
		t.Fatalf("signing input %s does not match the RFC token", input)
	}
	verifyJWT(t, strings.Join([]string{rfcJWTHead, rfcJWTBody, rfcJWTSig}, "."), rfcVAPIDKey)

	v, err := NewVAPID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := v.marshal()
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseVAPID(raw)
	if err != nil || again.PublicKey() != v.PublicKey() {
		t.Fatalf("reloaded key differs: %v", err)
	}
	now := time.Unix(1453480568, 0)
	h, err := again.Authorization("https://push.example.net", "mailto:push@example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	tok, key, ok := strings.Cut(strings.TrimPrefix(h, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(h, "vapid t=") || key != v.PublicKey() {
		t.Fatalf("authorization header %q", h)
	}
	c := verifyJWT(t, tok, key)
	if c.Aud != "https://push.example.net" || c.Sub != "mailto:push@example.com" || c.Exp <= now.Unix() || c.Exp > now.Add(24*time.Hour).Unix() {
		t.Fatalf("claims %+v", c)
	}
	if h2, _ := again.Authorization("https://push.example.net", "mailto:push@example.com", now.Add(time.Hour)); h2 != h {
		t.Fatal("token not reused within its lifetime")
	}
	if h3, _ := again.Authorization("https://push.example.net", "mailto:push@example.com", now.Add(11*time.Hour+time.Second)); h3 == h {
		t.Fatal("token reused close to expiry")
	}
}
