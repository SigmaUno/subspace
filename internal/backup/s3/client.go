package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultPartSize = 64 << 20
	MinPartSize     = 5 << 20
	maxParts        = 10000
	maxErrBody      = 64 << 10
	maxListBody     = 16 << 20
	metaSHA256      = "X-Amz-Meta-Sha256"
	defaultAttempts = 5
	baseBackoff     = 250 * time.Millisecond
	maxBackoff      = 10 * time.Second
)

var ErrNotFound = errors.New("s3: object not found")

type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("s3: status %d", e.Status)
	}
	return fmt.Sprintf("s3: status %d: %s: %s", e.Status, e.Code, e.Message)
}

func (e *Error) Is(target error) bool {
	return target == ErrNotFound && e.Status == http.StatusNotFound
}

func (e *Error) retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout
}

type Object struct {
	Key          string
	Size         int64
	ETag         string
	SHA256       string
	LastModified time.Time
}

type Config struct {
	Endpoint    string
	Region      string
	Bucket      string
	VirtualHost bool
	Credentials Credentials
	HTTP        *http.Client
	PartSize    int64
	Attempts    int
	Now         func() time.Time
	Sleep       func(ctx context.Context, d time.Duration) error
}

type Client struct {
	cfg    Config
	base   *url.URL
	signer Signer
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, errors.New("s3: endpoint must be an http(s) URL with a host")
	}
	if cfg.Bucket == "" || cfg.Region == "" {
		return nil, errors.New("s3: bucket and region are required")
	}
	if cfg.Credentials.AccessKey == "" || cfg.Credentials.SecretKey == "" {
		return nil, errors.New("s3: credentials are required")
	}
	if cfg.HTTP == nil {
		return nil, errors.New("s3: HTTP client is required")
	}
	if cfg.PartSize <= 0 {
		cfg.PartSize = DefaultPartSize
	}
	if cfg.Attempts <= 0 {
		cfg.Attempts = defaultAttempts
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleep
	}
	return &Client{
		cfg: cfg, base: &url.URL{Scheme: u.Scheme, Host: u.Host},
		signer: Signer{Credentials: cfg.Credentials, Region: cfg.Region, Service: "s3"},
	}, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) target(key string, q url.Values) *url.URL {
	u := &url.URL{Scheme: c.base.Scheme, Host: c.base.Host}
	p := "/" + key
	if c.cfg.VirtualHost {
		u.Host = c.cfg.Bucket + "." + c.base.Host
	} else {
		p = "/" + c.cfg.Bucket + p
	}
	u.Path = p
	u.RawPath = EscapePath(p)
	u.RawQuery = canonicalQuery(q)
	return u
}

type request struct {
	method  string
	key     string
	query   url.Values
	headers map[string]string
	body    func() io.Reader
	size    int64
	payload string
}

func (c *Client) do(ctx context.Context, rq request) (*http.Response, error) {
	var last error
	for attempt := 0; attempt < c.cfg.Attempts; attempt++ {
		if attempt > 0 {
			if err := c.cfg.Sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		resp, err := c.once(ctx, rq)
		if err == nil {
			return resp, nil
		}
		last = err
		if ctx.Err() != nil || !retryable(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("s3: giving up after %d attempts: %w", c.cfg.Attempts, last)
}

func backoff(attempt int) time.Duration {
	d := min(baseBackoff<<(attempt-1), maxBackoff)
	return d/2 + rand.N(d/2+1) //nolint:gosec // jitter only
}

func retryable(err error) bool {
	var se *Error
	if errors.As(err, &se) {
		return se.retryable()
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func (c *Client) once(ctx context.Context, rq request) (*http.Response, error) {
	u := c.target(rq.key, rq.query)
	var body io.Reader = http.NoBody
	if rq.body != nil && rq.size > 0 {
		body = rq.body()
	}
	req, err := http.NewRequestWithContext(ctx, rq.method, u.String(), body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = rq.size
	for k, v := range rq.headers {
		req.Header.Set(k, v)
	}
	payload := rq.payload
	if payload == "" {
		payload = EmptySHA256
	}
	c.signer.Sign(req, payload, c.cfg.Now())
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return nil, scrub(err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, parseError(resp)
}

func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func parseError(resp *http.Response) *Error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	return xmlError(resp.StatusCode, raw)
}

func xmlError(status int, raw []byte) *Error {
	e := &Error{Status: status}
	var doc struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(raw, &doc) == nil {
		e.Code, e.Message = doc.Code, doc.Message
	}
	return e
}

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
	_ = resp.Body.Close()
}

func (c *Client) Put(ctx context.Context, key string, src io.ReaderAt, size int64, sha256hex string) error {
	if size <= c.cfg.PartSize {
		return c.putSingle(ctx, key, src, size, sha256hex)
	}
	return c.putMultipart(ctx, key, src, size, sha256hex)
}

func (c *Client) putSingle(ctx context.Context, key string, src io.ReaderAt, size int64, sum string) error {
	resp, err := c.do(ctx, request{
		method: http.MethodPut, key: key, size: size, payload: sum,
		headers: map[string]string{metaSHA256: sum, "Content-Type": "application/octet-stream"},
		body:    func() io.Reader { return io.NewSectionReader(src, 0, size) },
	})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

func (c *Client) putMultipart(ctx context.Context, key string, src io.ReaderAt, size int64, sum string) (err error) {
	partSize := c.cfg.PartSize
	if (size+partSize-1)/partSize > maxParts {
		partSize = (size + maxParts - 1) / maxParts
	}
	id, err := c.initiate(ctx, key, sum)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if resp, aerr := c.do(actx, request{method: http.MethodDelete, key: key, query: url.Values{"uploadId": {id}}}); aerr == nil {
				drain(resp)
			}
		}
	}()
	var parts []completedPart
	for n, off := 1, int64(0); off < size; n, off = n+1, off+partSize {
		length := min(partSize, size-off)
		etag, perr := c.uploadPart(ctx, key, id, n, io.NewSectionReader(src, off, length), length)
		if perr != nil {
			return fmt.Errorf("part %d: %w", n, perr)
		}
		parts = append(parts, completedPart{PartNumber: n, ETag: etag})
	}
	return c.complete(ctx, key, id, parts)
}

func (c *Client) initiate(ctx context.Context, key, sum string) (string, error) {
	resp, err := c.do(ctx, request{
		method: http.MethodPost, key: key, query: url.Values{"uploads": {""}},
		headers: map[string]string{metaSHA256: sum, "Content-Type": "application/octet-stream"},
	})
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxErrBody)).Decode(&out); err != nil || out.UploadID == "" {
		return "", errors.New("s3: initiate multipart upload: no upload id in response")
	}
	return out.UploadID, nil
}

func (c *Client) uploadPart(ctx context.Context, key, id string, n int, sec *io.SectionReader, length int64) (string, error) {
	h, err := hashReader(sec, length)
	if err != nil {
		return "", err
	}
	resp, err := c.do(ctx, request{
		method: http.MethodPut, key: key, size: length, payload: h,
		query: url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {id}},
		body:  func() io.Reader { return io.NewSectionReader(sec, 0, length) },
	})
	if err != nil {
		return "", err
	}
	defer drain(resp)
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", errors.New("missing ETag in upload part response")
	}
	return etag, nil
}

type completedPart struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

func (c *Client) complete(ctx context.Context, key, id string, parts []completedPart) error {
	payload, err := xml.Marshal(struct {
		XMLName xml.Name        `xml:"CompleteMultipartUpload"`
		Parts   []completedPart `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, request{
		method: http.MethodPost, key: key, query: url.Values{"uploadId": {id}},
		size: int64(len(payload)), payload: Hash(payload),
		headers: map[string]string{"Content-Type": "application/xml"},
		body:    func() io.Reader { return bytes.NewReader(payload) },
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	if bytes.Contains(raw, []byte("<Error>")) {
		return xmlError(resp.StatusCode, raw)
	}
	return nil
}

func (c *Client) Head(ctx context.Context, key string) (Object, error) {
	resp, err := c.do(ctx, request{method: http.MethodHead, key: key})
	if err != nil {
		return Object{}, err
	}
	defer drain(resp)
	mod, _ := http.ParseTime(resp.Header.Get("Last-Modified"))
	return Object{
		Key: key, Size: resp.ContentLength, ETag: strings.Trim(resp.Header.Get("ETag"), `"`),
		SHA256: resp.Header.Get(metaSHA256), LastModified: mod,
	}, nil
}

func (c *Client) GetRange(ctx context.Context, key string, off, n int64) ([]byte, error) {
	if n <= 0 {
		return nil, nil
	}
	resp, err := c.do(ctx, request{
		method: http.MethodGet, key: key,
		headers: map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", off, off+n-1)},
	})
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusPartialContent && off != 0 {
		return nil, errors.New("s3: server ignored the range request")
	}
	buf, err := io.ReadAll(io.LimitReader(resp.Body, n))
	if err != nil {
		return nil, err
	}
	return buf, nil
}

func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, request{method: http.MethodGet, key: key})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, request{method: http.MethodDelete, key: key})
	if err != nil {
		return err
	}
	drain(resp)
	return nil
}

func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}}
		if prefix != "" {
			q.Set("prefix", prefix)
		}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, request{method: http.MethodGet, query: q})
		if err != nil {
			return nil, err
		}
		var page struct {
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
			Contents  []struct {
				Key          string    `xml:"Key"`
				Size         int64     `xml:"Size"`
				ETag         string    `xml:"ETag"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, maxListBody)).Decode(&page)
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("s3: list: %w", err)
		}
		for _, o := range page.Contents {
			out = append(out, Object{Key: o.Key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`), LastModified: o.LastModified})
		}
		if !page.Truncated || page.Next == "" {
			return out, nil
		}
		token = page.Next
	}
}
