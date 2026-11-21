package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/backup/s3"
	"github.com/SigmaUno/subspace/internal/backup/s3/s3test"
)

var creds = s3.Credentials{AccessKey: "test-access-key-id", SecretKey: "test-secret-access-key"}

func newClient(t *testing.T, srv *s3test.Server, virtual bool, part int64, slept *atomic.Int32) *s3.Client {
	t.Helper()
	c, err := s3.New(s3.Config{
		Endpoint: srv.Endpoint(), Region: "eu-west-1", Bucket: "backups", VirtualHost: virtual,
		Credentials: creds, HTTP: srv.Client(), PartSize: part, Attempts: 4,
		Sleep: func(context.Context, time.Duration) error {
			if slept != nil {
				slept.Add(1)
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func payload(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestObjectLifecycle(t *testing.T) {
	for _, virtual := range []bool{false, true} {
		t.Run(fmt.Sprintf("virtual=%v", virtual), func(t *testing.T) {
			srv := s3test.New(t, "backups", "eu-west-1", creds)
			c := newClient(t, srv, virtual, 0, nil)
			ctx := context.Background()
			data := payload(3000, 1)
			key := "prefix/with space/subspace-1.db.enc"
			if err := c.Put(ctx, key, bytes.NewReader(data), int64(len(data)), s3.Hash(data)); err != nil {
				t.Fatal(err)
			}
			obj, err := c.Head(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if obj.Size != 3000 || obj.SHA256 != s3.Hash(data) {
				t.Fatalf("head: %+v", obj)
			}
			part, err := c.GetRange(ctx, key, 100, 50)
			if err != nil || !bytes.Equal(part, data[100:150]) {
				t.Fatalf("range read: %v", err)
			}
			rc, err := c.Get(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			_, _ = got.ReadFrom(rc)
			_ = rc.Close()
			if !bytes.Equal(got.Bytes(), data) {
				t.Fatal("download differs")
			}
			list, err := c.List(ctx, "prefix/")
			if err != nil || len(list) != 1 || list[0].Key != key || list[0].Size != 3000 {
				t.Fatalf("list: %+v %v", list, err)
			}
			if err := c.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Head(ctx, key); !errors.Is(err, s3.ErrNotFound) {
				t.Fatalf("head after delete: %v", err)
			}
			if srv.Rejected != 0 {
				t.Fatalf("%d requests failed server-side signature verification", srv.Rejected)
			}
		})
	}
}

func TestZeroByteObject(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	c := newClient(t, srv, false, 0, nil)
	if err := c.Put(context.Background(), "empty", bytes.NewReader(nil), 0, s3.EmptySHA256); err != nil {
		t.Fatal(err)
	}
	if o, err := c.Head(context.Background(), "empty"); err != nil || o.Size != 0 {
		t.Fatalf("%+v %v", o, err)
	}
}

func TestMultipartUpload(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	c := newClient(t, srv, false, 1000, nil)
	ctx := context.Background()
	data := payload(4500, 2)
	if err := c.Put(ctx, "big", bytes.NewReader(data), int64(len(data)), s3.Hash(data)); err != nil {
		t.Fatal(err)
	}
	got, _ := srv.Get("big")
	if !bytes.Equal(got, data) {
		t.Fatal("assembled object differs")
	}
	var parts int
	for _, r := range srv.Requests() {
		if strings.HasPrefix(r, "PUT ") && strings.Contains(r, "partNumber=") {
			parts++
		}
	}
	if parts != 5 {
		t.Fatalf("uploaded %d parts, want 5", parts)
	}
	obj, err := c.Head(ctx, "big")
	if err != nil || obj.SHA256 != s3.Hash(data) || obj.Size != 4500 {
		t.Fatalf("head: %+v %v", obj, err)
	}
}

func TestMultipartAbortsOnFailure(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	srv.Inject(func(r *http.Request) int {
		if strings.Contains(r.URL.RawQuery, "partNumber=3") {
			return http.StatusInternalServerError
		}
		return 0
	})
	c := newClient(t, srv, false, 1000, nil)
	data := payload(4500, 3)
	err := c.Put(context.Background(), "big", bytes.NewReader(data), int64(len(data)), s3.Hash(data))
	if err == nil {
		t.Fatal("upload succeeded despite a failing part")
	}
	if srv.OpenUploads() != 0 {
		t.Fatal("failed multipart upload was not aborted")
	}
	if _, ok := srv.Get("big"); ok {
		t.Fatal("partial object visible")
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	var failures atomic.Int32
	srv.Inject(func(r *http.Request) int {
		if r.Method == http.MethodPut && failures.Add(1) <= 2 {
			return http.StatusServiceUnavailable
		}
		return 0
	})
	var slept atomic.Int32
	c := newClient(t, srv, false, 0, &slept)
	data := payload(100, 4)
	if err := c.Put(context.Background(), "k", bytes.NewReader(data), 100, s3.Hash(data)); err != nil {
		t.Fatal(err)
	}
	if slept.Load() != 2 {
		t.Fatalf("backed off %d times, want 2", slept.Load())
	}
	if got, _ := srv.Get("k"); !bytes.Equal(got, data) {
		t.Fatal("object differs after retries")
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	srv.Inject(func(*http.Request) int { return http.StatusInternalServerError })
	var slept atomic.Int32
	c := newClient(t, srv, false, 0, &slept)
	_, err := c.Head(context.Background(), "k")
	var se *s3.Error
	if !errors.As(err, &se) || se.Status != 500 {
		t.Fatalf("got %v", err)
	}
	if len(srv.Requests()) != 4 || slept.Load() != 3 {
		t.Fatalf("%d requests, %d backoffs", len(srv.Requests()), slept.Load())
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	bad := creds
	bad.SecretKey = "not-the-secret"
	c, err := s3.New(s3.Config{
		Endpoint: srv.Endpoint(), Region: "eu-west-1", Bucket: "backups", Credentials: bad, HTTP: srv.Client(),
		Sleep: func(context.Context, time.Duration) error { t.Error("slept on a 403"); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Head(context.Background(), "k")
	var se *s3.Error
	if !errors.As(err, &se) || se.Status != http.StatusForbidden {
		t.Fatalf("got %v", err)
	}
	if len(srv.Requests()) != 1 {
		t.Fatalf("%d requests", len(srv.Requests()))
	}
}

func TestPayloadHashMismatchRejected(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	c := newClient(t, srv, false, 0, nil)
	data := payload(100, 5)
	err := c.Put(context.Background(), "k", bytes.NewReader(data), 100, s3.Hash([]byte("something else")))
	var se *s3.Error
	if !errors.As(err, &se) || se.Code != "XAmzContentSHA256Mismatch" {
		t.Fatalf("got %v", err)
	}
	if _, ok := srv.Get("k"); ok {
		t.Fatal("object stored despite hash mismatch")
	}
}

func TestListPaginates(t *testing.T) {
	srv := s3test.New(t, "backups", "eu-west-1", creds)
	srv.PageSize = 2
	for i := range 5 {
		srv.Put(fmt.Sprintf("p/obj-%d", i), []byte("x"), "", time.Now())
	}
	srv.Put("other/x", []byte("x"), "", time.Now())
	list, err := newClient(t, srv, false, 0, nil).List(context.Background(), "p/")
	if err != nil || len(list) != 5 {
		t.Fatalf("%d objects, %v", len(list), err)
	}
}
