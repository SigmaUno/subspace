package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeMail struct {
	from, auth string
	rcpt       []string
	data       string
	tls        bool
}

type fakeSMTP struct {
	ln         net.Listener
	server     *tls.Config
	noSTARTTLS bool
	rejectRcpt bool
	mu         sync.Mutex
	mails      []fakeMail
}

func newFakeSMTP(t *testing.T) (*fakeSMTP, *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, server: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f, &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

func (f *fakeSMTP) addr() string { return f.ln.Addr().String() }

func (f *fakeSMTP) delivered() []fakeMail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeMail(nil), f.mails...)
}

func smtpArg(arg string) string {
	_, a, _ := strings.Cut(arg, "<")
	a, _, _ = strings.Cut(a, ">")
	return a
}

func (f *fakeSMTP) serve(raw net.Conn) {
	defer func() { _ = raw.Close() }()
	conn := raw
	r := bufio.NewReader(conn)
	say := func(line string) bool {
		_, err := io.WriteString(conn, line+"\r\n")
		return err == nil
	}
	say("220 fake.test ESMTP")
	var tx fakeMail
	tlsOn := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		switch strings.ToUpper(cmd) {
		case "EHLO":
			switch {
			case tlsOn:
				say("250-fake.test")
				say("250 AUTH PLAIN")
			case f.noSTARTTLS:
				say("250 fake.test")
			default:
				say("250-fake.test")
				say("250 STARTTLS")
			}
		case "STARTTLS":
			if f.noSTARTTLS || tlsOn || !say("220 ready for tls") {
				return
			}
			tc := tls.Server(raw, f.server)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, r, tlsOn = tc, bufio.NewReader(tc), true
		case "AUTH":
			_, enc, _ := strings.Cut(arg, " ")
			dec, err := base64.StdEncoding.DecodeString(enc)
			parts := strings.Split(string(dec), "\x00")
			if err != nil || len(parts) != 3 {
				say("501 bad auth")
				continue
			}
			tx.auth = parts[1] + ":" + parts[2]
			say("235 authenticated")
		case "MAIL":
			tx = fakeMail{from: smtpArg(arg), auth: tx.auth, tls: tlsOn}
			say("250 ok")
		case "RCPT":
			if f.rejectRcpt {
				say("550 no such user")
				continue
			}
			tx.rcpt = append(tx.rcpt, smtpArg(arg))
			say("250 ok")
		case "DATA":
			if !say("354 go ahead") {
				return
			}
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, "."))
			}
			tx.data = data.String()
			f.mu.Lock()
			f.mails = append(f.mails, tx)
			f.mu.Unlock()
			tx = fakeMail{auth: tx.auth, tls: tlsOn}
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("502 unsupported")
		}
	}
}

func mustSMTP(t *testing.T, url string, o SMTPOptions) Sink {
	t.Helper()
	s, err := NewSMTP(url, o)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSMTPDeliversPlainTextOverSTARTTLS(t *testing.T) {
	f, clientTLS := newFakeSMTP(t)
	sink := mustSMTP(t, "smtp://bot:app-pass@"+f.addr(), SMTPOptions{
		From: "Subspace <bot@sub.test>", To: []string{"owner@example.com", "ops@example.com"},
		STARTTLS: true, AllowPrivate: true, TLS: clientTLS,
	})
	n := Notification{
		ID: "P-1", Kind: "plan", Title: "Plan P-1 awaits approval", Text: "Agent rex proposed P-1 with 3 tasks",
		TS:      time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC),
		Actions: []Action{{Label: "Approve", URL: "https://sub.test/a/tok1"}, {Label: "Reject", URL: "https://sub.test/a/tok2"}},
	}
	if err := sink.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	got := f.delivered()
	if len(got) != 1 {
		t.Fatalf("delivered %d", len(got))
	}
	m := got[0]
	if !m.tls || m.auth != "bot:app-pass" || m.from != "bot@sub.test" || strings.Join(m.rcpt, ",") != "owner@example.com,ops@example.com" {
		t.Fatalf("envelope %+v", m)
	}
	msg, err := mail.ReadMessage(strings.NewReader(m.data))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("Subject") != "Plan P-1 awaits approval" || msg.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Fatalf("headers %v", msg.Header)
	}
	raw, _ := io.ReadAll(quotedprintable.NewReader(msg.Body))
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	for _, want := range []string{"Plan P-1 awaits approval\n", "Agent rex proposed P-1 with 3 tasks", "Approve: https://sub.test/a/tok1", "Reject: https://sub.test/a/tok2"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func TestSMTPRequiresSTARTTLSByDefault(t *testing.T) {
	f, clientTLS := newFakeSMTP(t)
	f.noSTARTTLS = true
	sink := mustSMTP(t, "smtp://"+f.addr(), SMTPOptions{
		From: "bot@sub.test", To: []string{"owner@example.com"}, STARTTLS: true, AllowPrivate: true, TLS: clientTLS,
	})
	err := sink.Send(context.Background(), Notification{ID: "Q-1", Title: "x"})
	var perm permanentError
	if !errors.Is(err, errNoSTARTTLS) || !errors.As(err, &perm) {
		t.Fatalf("expected permanent STARTTLS error, got %v", err)
	}
	if len(f.delivered()) != 0 {
		t.Fatal("message sent without TLS")
	}
}

func TestSMTPPlaintextOnlyWhenExplicitlyDisabled(t *testing.T) {
	f, _ := newFakeSMTP(t)
	f.noSTARTTLS = true
	if _, err := NewSMTP("smtp://bot:pw@"+f.addr(), SMTPOptions{From: "bot@sub.test", To: []string{"o@example.com"}, AllowPrivate: true}); err == nil {
		t.Fatal("credentials accepted without TLS")
	}
	sink := mustSMTP(t, "smtp://"+f.addr(), SMTPOptions{From: "bot@sub.test", To: []string{"o@example.com"}, AllowPrivate: true})
	if err := sink.Send(context.Background(), Notification{ID: "Q-1", Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if got := f.delivered(); len(got) != 1 || got[0].tls {
		t.Fatalf("delivered %+v", got)
	}
}

func TestSMTPRejectedRecipientIsPermanent(t *testing.T) {
	f, clientTLS := newFakeSMTP(t)
	f.rejectRcpt = true
	sink := mustSMTP(t, "smtp://"+f.addr(), SMTPOptions{
		From: "bot@sub.test", To: []string{"gone@example.com"}, STARTTLS: true, AllowPrivate: true, TLS: clientTLS,
	})
	var perm permanentError
	if err := sink.Send(context.Background(), Notification{ID: "Q-1", Title: "x"}); !errors.As(err, &perm) {
		t.Fatalf("expected permanent error, got %v", err)
	}
}

func TestSMTPGuardBlocksPrivateTargets(t *testing.T) {
	_, err := NewSMTP("smtp://127.0.0.1:2525", SMTPOptions{From: "bot@sub.test", To: []string{"o@example.com"}, STARTTLS: true})
	if !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("construction: %v", err)
	}
	sink, err := NewSMTP("smtp://localhost:1", SMTPOptions{From: "bot@sub.test", To: []string{"o@example.com"}, STARTTLS: true, AllowPrivate: false})
	if sink != nil || !errors.Is(err, ErrBlockedTarget) {
		t.Fatalf("localhost accepted: %v", err)
	}
}

func TestSMTPHeaderInjectionIsRejectedOrNeutralised(t *testing.T) {
	for _, addr := range []string{"o@example.com\r\nBcc: evil@example.com", "o@example.com\nRCPT TO:<x@y>", "\"o\r\n\" <o@example.com>"} {
		if _, err := NewSMTP("smtp://mail.example.com", SMTPOptions{From: "bot@sub.test", To: []string{addr}, STARTTLS: true}); err == nil {
			t.Errorf("recipient %q accepted", addr)
		}
		if _, err := NewSMTP("smtp://mail.example.com", SMTPOptions{From: addr, To: []string{"o@example.com"}, STARTTLS: true}); err == nil {
			t.Errorf("sender %q accepted", addr)
		}
	}

	allowed := map[string]bool{"From": true, "To": true, "Subject": true, "Date": true, "MIME-Version": true, "Content-Type": true, "Content-Transfer-Encoding": true}
	for _, title := range []string{
		"plain title",
		"evil\r\nBcc: victim@example.com",
		"evil\nBcc: victim@example.com\n\nforged body",
		"tab\tand\x00nul\x7f",
		"unicode ✓ title",
	} {
		raw := string(buildMail("bot@sub.test", []string{"o@example.com"}, Notification{Title: title, Text: "body"}))
		head, _, _ := strings.Cut(raw, "\r\n\r\n")
		for _, line := range strings.Split(head, "\r\n") {
			if line == "" || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			name, _, ok := strings.Cut(line, ": ")
			if !ok || !allowed[name] {
				t.Errorf("title %q produced header line %q", title, line)
			}
		}
	}
}
