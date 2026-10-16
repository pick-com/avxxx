// Package mailer sends multipart (plain text + HTML) mail over SMTP with
// net/smtp. Transport security is mandatory: port 465 uses implicit TLS,
// every other port must offer STARTTLS or the send fails before any
// credentials or message data are transmitted.
package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Message is one outgoing mail.
type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
	// ListUnsubscribe is a URL for the List-Unsubscribe header (RFC 2369);
	// empty omits the header.
	ListUnsubscribe string
}

// Config is the SMTP relay configuration.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string // RFC 5322 address, e.g. "REEL <noreply@example.com>"
	// TLS overrides the TLS client config (tests); ServerName defaults to Host.
	TLS     *tls.Config
	Timeout time.Duration
}

// SMTP sends messages through one relay.
type SMTP struct {
	cfg  Config
	from *mail.Address
	Now  func() time.Time
}

// New validates cfg.
func New(cfg Config) (*SMTP, error) {
	if cfg.Host == "" {
		return nil, errors.New("mailer: SMTP host required")
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		return nil, errors.New("mailer: bad SMTP port")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("mailer: MAIL_FROM: %w", err)
	}
	if (cfg.User == "") != (cfg.Password == "") {
		return nil, errors.New("mailer: SMTP user and password must be set together")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &SMTP{cfg: cfg, from: from}, nil
}

// PermanentError marks a 5xx SMTP reply: retrying the same message will not help.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// IsPermanent reports whether err is a permanent (5xx) SMTP failure.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// Send delivers m. Errors carrying a 5xx reply are wrapped in PermanentError.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	to, err := mail.ParseAddress(m.To)
	if err != nil {
		return &PermanentError{fmt.Errorf("mailer: bad recipient: %w", err)}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	body, err := Build(m, s.from, to, now(), messageID(s.from.Address))
	if err != nil {
		return &PermanentError{err}
	}
	err = s.deliver(ctx, to.Address, body)
	var te *textproto.Error
	if errors.As(err, &te) && te.Code >= 500 {
		return &PermanentError{err}
	}
	return err
}

func (s *SMTP) deliver(ctx context.Context, rcpt string, body []byte) error {
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.TLS != nil {
		tlsCfg = s.cfg.TLS.Clone()
		if tlsCfg.ServerName == "" {
			tlsCfg.ServerName = s.cfg.Host
		}
	}
	dialer := &net.Dialer{Timeout: s.cfg.Timeout}
	var conn net.Conn
	var err error
	implicitTLS := s.cfg.Port == 465
	if implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mailer: dial: %w", err)
	}
	deadline := time.Now().Add(s.cfg.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mailer: greeting: %w", err)
	}
	defer c.Close()
	if !implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("mailer: server does not offer STARTTLS; refusing to send in plaintext")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	if s.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("mailer: auth: %w", err)
		}
	}
	if err := c.Mail(s.from.Address); err != nil {
		return fmt.Errorf("mailer: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(rcpt); err != nil {
		return fmt.Errorf("mailer: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mailer: DATA: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("mailer: write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: end of data: %w", err)
	}
	return c.Quit()
}

func messageID(from string) string {
	b := make([]byte, 12)
	rand.Read(b)
	domain := "localhost"
	if i := strings.LastIndexByte(from, '@'); i >= 0 {
		domain = from[i+1:]
	}
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

// Build renders m as an RFC 5322 multipart/alternative message with CRLF
// line endings: a quoted-printable text/plain part, then text/html.
func Build(m Message, from, to *mail.Address, date time.Time, msgID string) ([]byte, error) {
	for _, v := range []string{m.Subject, m.ListUnsubscribe, msgID} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("mailer: header value contains a line break")
		}
	}
	if m.Text == "" {
		return nil, errors.New("mailer: text body required")
	}
	boundary := "b_" + strings.Trim(msgID, "<>")
	boundary = strings.NewReplacer("@", "_", ".", "_").Replace(boundary)
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", to.String())
	h("Subject", mime.BEncoding.Encode("utf-8", m.Subject))
	h("Date", date.Format(time.RFC1123Z))
	h("Message-ID", msgID)
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	if m.ListUnsubscribe != "" {
		h("List-Unsubscribe", "<"+m.ListUnsubscribe+">")
	}
	if m.HTML == "" {
		h("Content-Type", "text/plain; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, m.Text); err != nil {
			return nil, err
		}
		return b.Bytes(), nil
	}
	h("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ ctype, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		h("Content-Type", part.ctype+"; charset=utf-8")
		h("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		if err := writeQP(&b, part.body); err != nil {
			return nil, err
		}
		b.WriteString("\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func writeQP(b *bytes.Buffer, s string) error {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	w := quotedprintable.NewWriter(b)
	if _, err := w.Write([]byte(s)); err != nil {
		return err
	}
	return w.Close()
}
