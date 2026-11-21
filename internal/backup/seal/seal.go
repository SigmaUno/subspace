package seal

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
)

const (
	KeySize          = 32
	DefaultChunkSize = 1 << 20
	MaxChunkSize     = 16 << 20
	saltSize         = 32
	maxKeyIDLen      = 64
	hkdfInfo         = "subspace-backup-v1"
)

var magic = []byte("SSBKENC1")

var (
	ErrKey       = errors.New("seal: invalid key")
	ErrUnknownID = errors.New("seal: no key for key id")
	ErrFormat    = errors.New("seal: not a sealed backup")
	ErrAuth      = errors.New("seal: chunk failed authentication")
	ErrTruncated = errors.New("seal: stream truncated")
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type Key struct {
	ID     string
	Secret [KeySize]byte
}

func LoadKey(path, id string) (Key, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrKey, err)
	}
	if !fi.Mode().IsRegular() {
		return Key{}, fmt.Errorf("%w: %s is not a regular file", ErrKey, path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return Key{}, fmt.Errorf("%w: %s must not be accessible by group or others (mode %04o, want 0600)", ErrKey, path, fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return Key{}, fmt.Errorf("%w: %v", ErrKey, err)
	}
	return ParseKey(raw, id)
}

func ParseKey(raw []byte, id string) (Key, error) {
	var k Key
	text := bytes.TrimSpace(raw)
	switch {
	case len(raw) == KeySize:
		copy(k.Secret[:], raw)
	case len(text) == 2*KeySize:
		if _, err := hex.Decode(k.Secret[:], text); err != nil {
			return Key{}, fmt.Errorf("%w: not valid hex", ErrKey)
		}
	default:
		return Key{}, fmt.Errorf("%w: want %d raw bytes or %d hex characters", ErrKey, KeySize, 2*KeySize)
	}
	if id == "" {
		sum := sha256.Sum256(k.Secret[:])
		id = hex.EncodeToString(sum[:4])
	}
	if !keyIDPattern.MatchString(id) {
		return Key{}, fmt.Errorf("%w: key id must match %s", ErrKey, keyIDPattern)
	}
	k.ID = id
	return k, nil
}

func ValidKeyID(id string) bool { return keyIDPattern.MatchString(id) }

func (k Key) aead(salt []byte) (cipher.AEAD, error) {
	sub, err := hkdf.Key(sha256.New, k.Secret[:], salt, hkdfInfo, KeySize)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(sub)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func buildHeader(id string, chunk uint32, salt []byte) []byte {
	h := make([]byte, 0, len(magic)+1+len(id)+4+saltSize)
	h = append(h, magic...)
	h = append(h, byte(len(id))) //nolint:gosec // validated to at most 64 bytes
	h = append(h, id...)
	h = binary.BigEndian.AppendUint32(h, chunk)
	return append(h, salt...)
}

func chunkAAD(header []byte, index uint64, final bool) []byte {
	aad := make([]byte, 0, len(header)+9)
	aad = append(aad, header...)
	aad = binary.BigEndian.AppendUint64(aad, index)
	if final {
		return append(aad, 1)
	}
	return append(aad, 0)
}

func nonce(index uint64) []byte {
	n := make([]byte, 12)
	binary.BigEndian.PutUint64(n[4:], index)
	return n
}

type Writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	buf    []byte
	index  uint64
	closed bool
	err    error
}

func NewWriter(w io.Writer, k Key) (*Writer, error) {
	return newWriter(w, k, DefaultChunkSize)
}

func newWriter(w io.Writer, k Key, chunk int) (*Writer, error) {
	if !keyIDPattern.MatchString(k.ID) {
		return nil, fmt.Errorf("%w: key id must match %s", ErrKey, keyIDPattern)
	}
	if chunk <= 0 || chunk > MaxChunkSize {
		return nil, fmt.Errorf("seal: chunk size must be between 1 and %d", MaxChunkSize)
	}
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	header := buildHeader(k.ID, uint32(chunk), salt) //nolint:gosec // bounded above
	aead, err := k.aead(salt)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &Writer{w: w, aead: aead, header: header, buf: make([]byte, 0, chunk)}, nil
}

func (s *Writer) Write(p []byte) (int, error) {
	if s.closed {
		return 0, errors.New("seal: write after close")
	}
	if s.err != nil {
		return 0, s.err
	}
	n := 0
	for len(p) > 0 {
		if len(s.buf) == cap(s.buf) {
			if err := s.flush(false); err != nil {
				return n, err
			}
		}
		m := min(len(p), cap(s.buf)-len(s.buf))
		s.buf = append(s.buf, p[:m]...)
		p = p[m:]
		n += m
	}
	return n, nil
}

func (s *Writer) flush(final bool) error {
	out := s.aead.Seal(nil, nonce(s.index), s.buf, chunkAAD(s.header, s.index, final))
	if _, err := s.w.Write(out); err != nil {
		s.err = err
		return err
	}
	s.index++
	s.buf = s.buf[:0]
	return nil
}

func (s *Writer) Close() error {
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.err != nil {
		return s.err
	}
	return s.flush(true)
}

type Reader struct {
	br     *bufio.Reader
	aead   cipher.AEAD
	header []byte
	keyID  string
	chunk  int
	index  uint64
	ct     []byte
	plain  []byte
	pbuf   []byte
	done   bool
	err    error
}

func NewReader(r io.Reader, keys ...Key) (*Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	fixed := make([]byte, len(magic)+1)
	if _, err := io.ReadFull(br, fixed); err != nil {
		return nil, headerErr(err)
	}
	if !bytes.Equal(fixed[:len(magic)], magic) {
		return nil, ErrFormat
	}
	idLen := int(fixed[len(magic)])
	rest := make([]byte, idLen+4+saltSize)
	if _, err := io.ReadFull(br, rest); err != nil {
		return nil, headerErr(err)
	}
	id := string(rest[:idLen])
	chunk := binary.BigEndian.Uint32(rest[idLen : idLen+4])
	salt := rest[idLen+4:]
	if !keyIDPattern.MatchString(id) || chunk == 0 || chunk > MaxChunkSize {
		return nil, ErrFormat
	}
	var key *Key
	for i := range keys {
		if keys[i].ID == id {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownID, id)
	}
	header := append(fixed, rest...)
	aead, err := key.aead(salt)
	if err != nil {
		return nil, err
	}
	return &Reader{
		br: br, aead: aead, header: header, keyID: id, chunk: int(chunk),
		ct: make([]byte, int(chunk)+aead.Overhead()),
	}, nil
}

func headerErr(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrFormat
	}
	return err
}

func (s *Reader) KeyID() string { return s.keyID }

func (s *Reader) Read(p []byte) (int, error) {
	for len(s.plain) == 0 {
		if s.err != nil {
			return 0, s.err
		}
		if s.done {
			return 0, io.EOF
		}
		s.err = s.next()
	}
	n := copy(p, s.plain)
	s.plain = s.plain[n:]
	return n, nil
}

func (s *Reader) next() error {
	n, err := io.ReadFull(s.br, s.ct)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	last := n < len(s.ct)
	if !last {
		if _, perr := s.br.Peek(1); errors.Is(perr, io.EOF) {
			last = true
		} else if perr != nil {
			return perr
		}
	}
	if n < s.aead.Overhead() {
		return ErrTruncated
	}
	ct := s.ct[:n]
	plain, err := s.aead.Open(s.pbuf[:0], nonce(s.index), ct, chunkAAD(s.header, s.index, last))
	if err != nil {
		if last {
			if _, alt := s.aead.Open(nil, nonce(s.index), ct, chunkAAD(s.header, s.index, false)); alt == nil {
				return ErrTruncated
			}
		}
		return fmt.Errorf("%w (chunk %d)", ErrAuth, s.index)
	}
	s.pbuf, s.plain = plain, plain
	s.index++
	s.done = last
	return nil
}

func Encrypt(dst io.Writer, src io.Reader, k Key) error {
	w, err := NewWriter(dst, k)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, src); err != nil {
		return err
	}
	return w.Close()
}

func Decrypt(dst io.Writer, src io.Reader, keys ...Key) error {
	r, err := NewReader(src, keys...)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, r)
	return err
}
