package webhookout

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

const SignaturePrefix = "sha256="

func Sign(secret string, timestamp int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(strconv.FormatInt(timestamp, 10)))
	m.Write([]byte{'.'})
	m.Write(body)
	return SignaturePrefix + hex.EncodeToString(m.Sum(nil))
}

func Verify(secret, timestamp string, body []byte, signature string) bool {
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(Sign(secret, ts, body)), []byte(signature))
}
