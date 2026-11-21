package s3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm     = "AWS4-HMAC-SHA256"
	amzDateLayout = "20060102T150405Z"
	dateLayout    = "20060102"
	EmptySHA256   = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
}

type Signer struct {
	Credentials
	Region  string
	Service string
}

func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s Signer) Sign(r *http.Request, payloadHash string, now time.Time) {
	now = now.UTC()
	r.Header.Set("X-Amz-Date", now.Format(amzDateLayout))
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.SessionToken != "" {
		r.Header.Set("X-Amz-Security-Token", s.SessionToken)
	}
	canonical, signed := canonicalRequest(r, payloadHash)
	scope := strings.Join([]string{now.Format(dateLayout), s.Region, s.Service, "aws4_request"}, "/")
	toSign := strings.Join([]string{algorithm, now.Format(amzDateLayout), scope, Hash([]byte(canonical))}, "\n")
	sig := hex.EncodeToString(mac(s.signingKey(now), toSign))
	r.Header.Set("Authorization", algorithm+" Credential="+s.AccessKey+"/"+scope+",SignedHeaders="+signed+",Signature="+sig)
}

func (s Signer) signingKey(now time.Time) []byte {
	k := mac([]byte("AWS4"+s.SecretKey), now.Format(dateLayout))
	k = mac(k, s.Region)
	k = mac(k, s.Service)
	return mac(k, "aws4_request")
}

func mac(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}

func canonicalRequest(r *http.Request, payloadHash string) (canonical, signedHeaders string) {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	headers := map[string]string{"host": host}
	for name, vals := range r.Header {
		l := strings.ToLower(name)
		if !signable(l) {
			continue
		}
		trimmed := make([]string, len(vals))
		for i, v := range vals {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		headers[l] = strings.Join(trimmed, ",")
	}
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var hb strings.Builder
	for _, n := range names {
		hb.WriteString(n + ":" + headers[n] + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	canonical = strings.Join([]string{
		r.Method, canonicalPath(r.URL.Path), canonicalQuery(r.URL.Query()), hb.String(), signedHeaders, payloadHash,
	}, "\n")
	return canonical, signedHeaders
}

func signable(lower string) bool {
	switch lower {
	case "date", "range", "content-md5", "content-type":
		return true
	}
	return strings.HasPrefix(lower, "x-amz-")
}

func canonicalPath(p string) string {
	if p == "" {
		return "/"
	}
	return EscapePath(p)
}

func canonicalQuery(q url.Values) string {
	type pair struct{ k, v string }
	var pairs []pair
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, pair{escape(k), escape(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = escape(s)
	}
	return strings.Join(segs, "/")
}

func escape(s string) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexUpper[c>>4])
		b.WriteByte(hexUpper[c&15])
	}
	return b.String()
}

func hashReader(r io.ReaderAt, size int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(r, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
