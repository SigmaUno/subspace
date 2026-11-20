// Package auth handles tokens: generation, hashing, owner check, agent lookup cache.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"

	"github.com/SigmaUno/subspace/internal/model"
)

// TokenPrefix prefixes every agent token.
const TokenPrefix = "ssp_"

// NewToken returns a new random agent token (32 random bytes, base64url).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// Hash returns the hex SHA-256 of a token.
func Hash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// OwnerMatch compares a presented token with the owner token in constant time.
func OwnerMatch(presented, owner string) bool {
	a, b := sha256.Sum256([]byte(presented)), sha256.Sum256([]byte(owner))
	return owner != "" && subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// AgentLookup resolves a bearer token to an agent (nil if unknown/revoked).
type AgentLookup interface {
	LookupToken(token string) (*model.Agent, error)
}

// TODO(split B2): cached AgentLookup (positive + bounded negative cache, Invalidate(id)) in cache.go.
