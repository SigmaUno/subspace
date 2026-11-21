package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/SigmaUno/subspace/internal/netguard"
)

var (
	errNoSTARTTLS = errors.New("notify: smtp server does not offer STARTTLS")
	errPlainAuth  = errors.New("notify: smtp credentials require TLS")
)

const smtpTimeout = 30 * time.Second

type SMTPOptions struct {
	From         string
	To           []string
	STARTTLS     bool
	AllowPrivate bool
	TLS          *tls.Config
}

type smtpSink struct {
	host      string
	addr      string
	user      string
	pass      string
	implicit  bool
	starttls  bool
	from      string
	to        []string
	dialer    *netguard.Guard
	tlsConfig *tls.Config
}

func NewSMTP(raw string, o SMTPOptions) (Sink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("notify: invalid smtp url")
	}
	s := &smtpSink{starttls: o.STARTTLS, dialer: newGuard(o.AllowPrivate), tlsConfig: o.TLS}
	if s.tlsConfig == nil {
		s.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	var port string
	switch u.Scheme {
	case "smtp":
		port = "587"
	case "smtps":
		s.implicit = true
		port = "465"
	default:
		return nil, fmt.Errorf("notify: unsupported smtp scheme %q", u.Scheme)
	}
	s.host = strings.ToLower(u.Hostname())
	if err := checkHost(s.host, o.AllowPrivate); err != nil {
		return nil, err
	}
	if p := u.Port(); p != "" {
		port = p
	}
	s.addr = net.JoinHostPort(s.host, port)
	if u.User != nil {
		if !s.implicit && !s.starttls {
			return nil, errPlainAuth
		}
		s.user = u.User.Username()
		s.pass, _ = u.User.Password()
	}
	from, err := mail.ParseAddress(o.From)
	if err != nil {
		return nil, errors.New("notify: invalid smtp from address")
	}
	s.from = from.Address
	for _, t := range o.To {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return nil, errors.New("notify: invalid smtp recipient address")
		}
		s.to = append(s.to, a.Address)
	}
	if len(s.to) == 0 {
		return nil, errors.New("notify: smtp needs at least one recipient")
	}
	return s, nil
}

func (s *smtpSink) Name() string { return "smtp" }

func (s *smtpSink) Send(ctx context.Context, n Notification) error {
	err := s.deliver(ctx, buildMail(s.from, s.to, n))
	if err == nil {
		return nil
	}
	var tpErr *textproto.Error
	if errors.Is(err, ErrBlockedTarget) || errors.Is(err, errNoSTARTTLS) || errors.Is(err, errPlainAuth) ||
		(errors.As(err, &tpErr) && tpErr.Code >= 500) {
		return permanentError{err}
	}
	return err
}

func (s *smtpSink) deliver(ctx context.Context, msg []byte) error {
	conn, err := s.dialer.DialContext(ctx, "tcp", s.addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(smtpTimeout))

	wire := conn
	if s.implicit {
		wire = tls.Client(conn, s.clientTLS())
	}
	c, err := smtp.NewClient(wire, s.host)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	if s.starttls {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errNoSTARTTLS
		}
		if err := c.StartTLS(s.clientTLS()); err != nil {
			return err
		}
	}
	if s.user != "" {
		if err := c.Auth(plainAuth{s.user, s.pass}); err != nil {
			return err
		}
	}
	if err := c.Mail(s.from); err != nil {
		return err
	}
	for _, to := range s.to {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	_ = c.Quit()
	return nil
}

func (s *smtpSink) clientTLS() *tls.Config {
	c := s.tlsConfig.Clone()
	if c.ServerName == "" {
		c.ServerName = s.host
	}
	return c
}

type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}

func (plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("notify: unexpected smtp auth challenge")
	}
	return nil, nil
}

func buildMail(from string, to []string, n Notification) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", headerText(n.Title)))
	fmt.Fprintf(&b, "Date: %s\r\n", n.TS.UTC().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&b)
	_, _ = qp.Write([]byte(mailBody(n)))
	_ = qp.Close()
	return []byte(b.String())
}

func mailBody(n Notification) string {
	var b strings.Builder
	b.WriteString(n.Title + "\n\n" + n.Text + "\n")
	if len(n.Actions) > 0 {
		b.WriteString("\n")
		for _, a := range n.Actions {
			fmt.Fprintf(&b, "%s: %s\n", a.Label, a.URL)
		}
	}
	return b.String()
}

func headerText(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
}
