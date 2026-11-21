package s3test

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // S3 ETags are MD5
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/s3"
)

type Object struct {
	Data     []byte
	SHA256   string
	ETag     string
	Modified time.Time
}

type upload struct {
	key   string
	sha   string
	parts map[int][]byte
}

type Server struct {
	*httptest.Server
	Bucket   string
	Region   string
	Creds    s3.Credentials
	PageSize int

	mu       sync.Mutex
	objects  map[string]*Object
	uploads  map[string]*upload
	log      []string
	inject   func(r *http.Request) int
	nextID   int
	now      func() time.Time
	Rejected int
}

func New(t testing.TB, bucket, region string, creds s3.Credentials) *Server {
	t.Helper()
	s := &Server{
		Bucket: bucket, Region: region, Creds: creds,
		objects: map[string]*Object{}, uploads: map[string]*upload{}, now: time.Now,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

func (s *Server) Endpoint() string { return "http://s3.example.test" }

func (s *Server) Client() *http.Client {
	addr := s.Listener.Addr().String()
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		DisableKeepAlives: true,
	}}
}

func (s *Server) Inject(fn func(r *http.Request) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inject = fn
}

func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func (s *Server) Get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	if !ok {
		return nil, false
	}
	return bytes.Clone(o.Data), true
}

func (s *Server) Put(key string, data []byte, sha string, modified time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = &Object{Data: bytes.Clone(data), SHA256: sha, ETag: etag(data), Modified: modified}
}

func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *Server) Mutate(key string, fn func([]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.objects[key].Data)
}

func (s *Server) OpenUploads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.uploads)
}

func etag(b []byte) string {
	sum := md5.Sum(b) //nolint:gosec // S3 ETags are MD5
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.log = append(s.log, r.Method+" "+r.URL.RequestURI())
	inject := s.inject
	s.mu.Unlock()
	if inject != nil {
		if code := inject(r); code != 0 {
			s.fail(w, code, "InjectedFault", "injected")
			return
		}
	}
	if code, msg := s.authenticate(r, body); code != "" {
		s.mu.Lock()
		s.Rejected++
		s.mu.Unlock()
		status := http.StatusForbidden
		if code == "XAmzContentSHA256Mismatch" {
			status = http.StatusBadRequest
		}
		s.fail(w, status, code, msg)
		return
	}
	key, ok := s.keyOf(r)
	if !ok {
		s.fail(w, http.StatusNotFound, "NoSuchBucket", "no such bucket")
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case key == "" && r.Method == http.MethodGet:
		s.list(w, q)
	case r.Method == http.MethodPost && q.Has("uploads"):
		s.initiate(w, key, r)
	case r.Method == http.MethodPost && q.Has("uploadId"):
		s.complete(w, key, q.Get("uploadId"), body)
	case r.Method == http.MethodPut && q.Has("uploadId"):
		s.part(w, q, body)
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		delete(s.uploads, q.Get("uploadId"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPut:
		s.objects[key] = &Object{Data: body, SHA256: r.Header.Get("X-Amz-Meta-Sha256"), ETag: etag(body), Modified: s.now()}
		w.Header().Set("ETag", etag(body))
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		s.read(w, r, key)
	case r.Method == http.MethodDelete:
		delete(s.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		s.failLocked(w, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func (s *Server) keyOf(r *http.Request) (string, bool) {
	host := r.Host
	path := strings.TrimPrefix(r.URL.Path, "/")
	if strings.HasPrefix(host, s.Bucket+".") {
		return path, true
	}
	bucket, key, _ := strings.Cut(path, "/")
	return key, bucket == s.Bucket
}

func (s *Server) authenticate(r *http.Request, body []byte) (code, msg string) {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, "AWS4-HMAC-SHA256 ")
	if !ok {
		return "AccessDenied", "missing signature"
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	if !strings.HasPrefix(fields["Credential"], s.Creds.AccessKey+"/") {
		return "InvalidAccessKeyId", "unknown access key"
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "AccessDenied", "bad x-amz-date"
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload != s3.Hash(body) {
		return "XAmzContentSHA256Mismatch", "payload hash does not match the body"
	}
	clone := r.Clone(r.Context())
	clone.Header.Del("Authorization")
	signer := s3.Signer{Credentials: s.Creds, Region: s.Region, Service: "s3"}
	signer.Sign(clone, payload, at)
	if clone.Header.Get("Authorization") != auth {
		return "SignatureDoesNotMatch", "the request signature does not match"
	}
	return "", ""
}

func (s *Server) fail(w http.ResponseWriter, status int, code, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failLocked(w, status, code, msg) //nolint:gosec // test server, XML error body
}

func (s *Server) failLocked(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, msg) //nolint:gosec // test server, fixed error strings
}

func (s *Server) read(w http.ResponseWriter, r *http.Request, key string) {
	o, ok := s.objects[key]
	if !ok {
		s.failLocked(w, http.StatusNotFound, "NoSuchKey", "no such key")
		return
	}
	w.Header().Set("ETag", o.ETag)
	w.Header().Set("Last-Modified", o.Modified.UTC().Format(http.TimeFormat))
	if o.SHA256 != "" {
		w.Header().Set("X-Amz-Meta-Sha256", o.SHA256)
	}
	data := o.Data
	status := http.StatusOK
	if spec, ok := strings.CutPrefix(r.Header.Get("Range"), "bytes="); ok {
		from, to, _ := strings.Cut(spec, "-")
		start, _ := strconv.ParseInt(from, 10, 64)
		end, _ := strconv.ParseInt(to, 10, 64)
		if start >= int64(len(data)) {
			s.failLocked(w, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "range not satisfiable")
			return
		}
		end = min(end, int64(len(data))-1)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(data)))
		data = data[start : end+1]
		status = http.StatusPartialContent
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}

func (s *Server) list(w http.ResponseWriter, q map[string][]string) {
	prefix := first(q["prefix"])
	after := first(q["continuation-token"])
	var keys []string
	for k := range s.objects {
		if strings.HasPrefix(k, prefix) && k > after {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	page := s.PageSize
	if page <= 0 {
		page = 1000
	}
	truncated := len(keys) > page
	if truncated {
		keys = keys[:page]
	}
	var b strings.Builder
	b.WriteString("<ListBucketResult>")
	fmt.Fprintf(&b, "<IsTruncated>%t</IsTruncated>", truncated)
	if truncated {
		fmt.Fprintf(&b, "<NextContinuationToken>%s</NextContinuationToken>", escapeXML(keys[len(keys)-1]))
	}
	for _, k := range keys {
		o := s.objects[k]
		fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size><ETag>%s</ETag><LastModified>%s</LastModified></Contents>",
			escapeXML(k), len(o.Data), escapeXML(o.ETag), o.Modified.UTC().Format(time.RFC3339))
	}
	b.WriteString("</ListBucketResult>")
	w.Header().Set("Content-Type", "application/xml")
	_, _ = io.WriteString(w, b.String())
}

func escapeXML(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (s *Server) initiate(w http.ResponseWriter, key string, r *http.Request) {
	s.nextID++
	id := fmt.Sprintf("upload-%d", s.nextID)
	s.uploads[id] = &upload{key: key, sha: r.Header.Get("X-Amz-Meta-Sha256"), parts: map[int][]byte{}}
	_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
}

func (s *Server) part(w http.ResponseWriter, q map[string][]string, body []byte) {
	up, ok := s.uploads[first(q["uploadId"])]
	if !ok {
		s.failLocked(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
		return
	}
	n, _ := strconv.Atoi(first(q["partNumber"]))
	up.parts[n] = bytes.Clone(body)
	w.Header().Set("ETag", etag(body))
}

func (s *Server) complete(w http.ResponseWriter, key, id string, body []byte) {
	up, ok := s.uploads[id]
	if !ok || up.key != key {
		s.failLocked(w, http.StatusNotFound, "NoSuchUpload", "no such upload")
		return
	}
	var req struct {
		Parts []struct {
			PartNumber int    `xml:"PartNumber"`
			ETag       string `xml:"ETag"`
		} `xml:"Part"`
	}
	if err := xml.Unmarshal(body, &req); err != nil || len(req.Parts) == 0 {
		s.failLocked(w, http.StatusBadRequest, "MalformedXML", "bad completion document")
		return
	}
	var data []byte
	last := 0
	for _, p := range req.Parts {
		part, ok := up.parts[p.PartNumber]
		if !ok || p.PartNumber <= last || p.ETag != etag(part) {
			s.failLocked(w, http.StatusBadRequest, "InvalidPart", fmt.Sprintf("part %d", p.PartNumber))
			return
		}
		last = p.PartNumber
		data = append(data, part...)
	}
	delete(s.uploads, id)
	s.objects[key] = &Object{Data: data, SHA256: up.sha, ETag: fmt.Sprintf(`"multipart-%d"`, len(req.Parts)), Modified: s.now()}
	_, _ = fmt.Fprintf(w, "<CompleteMultipartUploadResult><Key>%s</Key></CompleteMultipartUploadResult>", escapeXML(key))
}
