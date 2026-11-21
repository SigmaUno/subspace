package seal

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

const testChunk = 64

func testKey(t *testing.T, id string, fill byte) Key {
	t.Helper()
	k, err := ParseKey(bytes.Repeat([]byte{fill}, KeySize), id)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func sealBytes(t *testing.T, k Key, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := newWriter(&out, k, testChunk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func open(sealed []byte, keys ...Key) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(sealed), keys...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func headerLen(k Key) int { return len(magic) + 1 + len(k.ID) + 4 + saltSize }

func frame() int { return testChunk + 16 }

func TestRoundTripSizes(t *testing.T) {
	k := testKey(t, "k1", 7)
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, testChunk - 1, testChunk, testChunk + 1, 3 * testChunk, 3*testChunk + 5, 10_000} {
		plain := make([]byte, n)
		rng.Read(plain)
		got, err := open(sealBytes(t, k, plain), k)
		if err != nil {
			t.Fatalf("size %d: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d: round trip differs", n)
		}
	}
}

func TestTamperAnyByteFails(t *testing.T) {
	k := testKey(t, "k1", 7)
	plain := bytes.Repeat([]byte("subspace"), 40)
	sealed := sealBytes(t, k, plain)
	for i := range sealed {
		mut := bytes.Clone(sealed)
		mut[i] ^= 0x01
		got, err := open(mut, k)
		if err == nil && bytes.Equal(got, plain) {
			t.Fatalf("flipping byte %d went unnoticed", i)
		}
		if err == nil {
			t.Fatalf("flipping byte %d returned data without error", i)
		}
	}
}

func TestTruncationDetected(t *testing.T) {
	k := testKey(t, "k1", 7)
	plain := bytes.Repeat([]byte("x"), 5*testChunk+10)
	sealed := sealBytes(t, k, plain)
	h, f := headerLen(k), frame()
	for _, cut := range []int{h + f, h + 2*f, h + 5*f, len(sealed) - 1, len(sealed) - 20, h + 3*f + 7} {
		_, err := open(sealed[:cut], k)
		if err == nil {
			t.Fatalf("truncation at %d of %d accepted", cut, len(sealed))
		}
	}
	if _, err := open(sealed[:h+2*f], k); !errors.Is(err, ErrTruncated) {
		t.Fatalf("chunk-boundary truncation: got %v, want ErrTruncated", err)
	}
	if _, err := open(sealed[:h], k); !errors.Is(err, ErrTruncated) {
		t.Fatalf("header-only stream: got %v, want ErrTruncated", err)
	}
}

func TestEmptyPlaintext(t *testing.T) {
	k := testKey(t, "k1", 7)
	sealed := sealBytes(t, k, nil)
	if got, err := open(sealed, k); err != nil || len(got) != 0 {
		t.Fatalf("empty round trip: %v %d", err, len(got))
	}
}

func TestReorderDetected(t *testing.T) {
	k := testKey(t, "k1", 7)
	plain := make([]byte, 4*testChunk+3)
	rand.New(rand.NewSource(2)).Read(plain)
	sealed := sealBytes(t, k, plain)
	h, f := headerLen(k), frame()
	mut := bytes.Clone(sealed)
	a, b := mut[h:h+f], mut[h+f:h+2*f]
	tmp := bytes.Clone(a)
	copy(a, b)
	copy(b, tmp)
	if _, err := open(mut, k); !errors.Is(err, ErrAuth) {
		t.Fatalf("swapped chunks: got %v, want ErrAuth", err)
	}
}

func TestDuplicatedChunkDetected(t *testing.T) {
	k := testKey(t, "k1", 7)
	plain := make([]byte, 4*testChunk+3)
	sealed := sealBytes(t, k, plain)
	h, f := headerLen(k), frame()
	var mut []byte
	mut = append(mut, sealed[:h+f]...)
	mut = append(mut, sealed[h:h+f]...)
	mut = append(mut, sealed[h+f:]...)
	if _, err := open(mut, k); err == nil {
		t.Fatal("duplicated chunk accepted")
	}
}

func TestTrailingDataDetected(t *testing.T) {
	k := testKey(t, "k1", 7)
	sealed := sealBytes(t, k, bytes.Repeat([]byte("y"), 2*testChunk))
	if _, err := open(append(bytes.Clone(sealed), 0), k); err == nil {
		t.Fatal("trailing byte accepted")
	}
	short := sealBytes(t, k, []byte("tiny"))
	if _, err := open(append(bytes.Clone(short), make([]byte, 32)...), k); err == nil {
		t.Fatal("trailing data after a short final chunk accepted")
	}
}

func TestChunksFromAnotherFileRejected(t *testing.T) {
	k := testKey(t, "k1", 7)
	plain := bytes.Repeat([]byte("z"), 3*testChunk)
	a, b := sealBytes(t, k, plain), sealBytes(t, k, plain)
	h, f := headerLen(k), frame()
	mut := bytes.Clone(a)
	copy(mut[h+f:h+2*f], b[h+f:h+2*f])
	if _, err := open(mut, k); !errors.Is(err, ErrAuth) {
		t.Fatalf("spliced chunk: got %v, want ErrAuth", err)
	}
}

func TestKeySelection(t *testing.T) {
	k1, k2 := testKey(t, "old", 1), testKey(t, "new", 2)
	sealed := sealBytes(t, k2, []byte("secret"))
	got, err := open(sealed, k1, k2)
	if err != nil || string(got) != "secret" {
		t.Fatalf("key ring lookup: %q %v", got, err)
	}
	if _, err := open(sealed, k1); !errors.Is(err, ErrUnknownID) {
		t.Fatalf("missing key id: got %v", err)
	}
	impostor := testKey(t, "new", 9)
	if _, err := open(sealed, impostor); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key with matching id: got %v", err)
	}
	r, err := NewReader(bytes.NewReader(sealed), k2)
	if err != nil || r.KeyID() != "new" {
		t.Fatalf("key id from header: %v %v", r, err)
	}
}

func TestNotSealed(t *testing.T) {
	k := testKey(t, "k1", 7)
	for _, in := range [][]byte{nil, []byte("SQLite format 3\x00"), bytes.Repeat([]byte{0}, 200)} {
		if _, err := NewReader(bytes.NewReader(in), k); !errors.Is(err, ErrFormat) {
			t.Fatalf("got %v, want ErrFormat", err)
		}
	}
}

func TestSubkeyPerFile(t *testing.T) {
	k := testKey(t, "k1", 7)
	a, b := sealBytes(t, k, []byte("same")), sealBytes(t, k, []byte("same"))
	if bytes.Equal(a[headerLen(k):], b[headerLen(k):]) {
		t.Fatal("two files with the same plaintext share ciphertext")
	}
}

func TestLoadKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup.key")
	hexKey := bytes.Repeat([]byte("ab"), KeySize)
	if err := os.WriteFile(path, hexKey, 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKey(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(k.ID) != 8 {
		t.Fatalf("default key id %q", k.ID)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(path, ""); !errors.Is(err, ErrKey) {
		t.Fatalf("group-readable key accepted: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKey(path, ""); !errors.Is(err, ErrKey) {
		t.Fatalf("short key accepted: %v", err)
	}
	if _, err := ParseKey(hexKey, "bad id!"); !errors.Is(err, ErrKey) {
		t.Fatalf("bad key id accepted: %v", err)
	}
}
