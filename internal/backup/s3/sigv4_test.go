package s3

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

var awsExample = Signer{
	Credentials: Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
	Region:      "us-east-1",
	Service:     "s3",
}

func TestSigV4AWSDocumentationVectors(t *testing.T) {
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	body := "Welcome to Amazon S3."
	tests := []struct {
		name    string
		method  string
		url     string
		headers map[string]string
		payload string
		signed  string
		sig     string
	}{
		{
			name: "get object", method: "GET", url: "https://examplebucket.s3.amazonaws.com/test.txt",
			headers: map[string]string{"Range": "bytes=0-9"}, payload: EmptySHA256,
			signed: "host;range;x-amz-content-sha256;x-amz-date",
			sig:    "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			name: "put object", method: "PUT", url: "https://examplebucket.s3.amazonaws.com/test$file.text",
			headers: map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"},
			payload: Hash([]byte(body)),
			signed:  "date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class",
			sig:     "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			name: "get bucket lifecycle", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?lifecycle",
			payload: EmptySHA256,
			signed:  "host;x-amz-content-sha256;x-amz-date",
			sig:     "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			name: "list objects", method: "GET", url: "https://examplebucket.s3.amazonaws.com/?max-keys=2&prefix=J",
			payload: EmptySHA256,
			signed:  "host;x-amz-content-sha256;x-amz-date",
			sig:     "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := http.NewRequest(tc.method, tc.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			awsExample.Sign(r, tc.payload, at)
			want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=" + tc.signed + ",Signature=" + tc.sig
			if got := r.Header.Get("Authorization"); got != want {
				t.Fatalf("authorization\n got %s\nwant %s", got, want)
			}
		})
	}
}

func TestSigV4SessionTokenIsSigned(t *testing.T) {
	s := awsExample
	s.SessionToken = "token"
	r, _ := http.NewRequest("GET", "https://examplebucket.s3.amazonaws.com/x", nil)
	s.Sign(r, EmptySHA256, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	if auth := r.Header.Get("Authorization"); !strings.Contains(auth, "x-amz-security-token") {
		t.Fatalf("session token header not signed: %s", auth)
	}
}

func TestEscapePath(t *testing.T) {
	for in, want := range map[string]string{
		"/a b/c+d/é": "/a%20b/c%2Bd/%C3%A9",
		"/x~y_z-.1":  "/x~y_z-.1",
		"/test$file": "/test%24file",
	} {
		if got := EscapePath(in); got != want {
			t.Errorf("EscapePath(%q) = %q, want %q", in, got, want)
		}
	}
}
