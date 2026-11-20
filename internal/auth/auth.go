package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"

	"github.com/SigmaUno/subspace/internal/model"
)

const TokenPrefix = "ssp_"

var ErrTokenExpired = errors.New("auth: token expired")

func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func OwnerMatch(presented, owner string) bool {
	a, b := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(owner))
	return owner != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

type AgentLookup interface {
	LookupToken(token string) (*model.Agent, error)
}
