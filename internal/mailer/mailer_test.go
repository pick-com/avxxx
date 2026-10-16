package mailer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal in-process SMTP server (EHLO, STARTTLS, AUTH PLAIN,
// MAIL, RCPT, DATA, QUIT) recording what it saw.
type fakeSMTP struct {
	ln        net.Listener
	tlsCfg    *tls.Config
	starttls  bool   // advertise STARTTLS
	rcptReply string // reply to RCPT TO (default 250)

	mu       sync.Mutex
	cmds     []string
	authUser string
	authPass string
	authTLS  bool // AUTH arrived over TLS
	from, to string
	data     []byte
}

func selfSigned(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		KeyUsage:    x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func startFake(t *testing.T, starttls bool) (*fakeSMTP, *x509.CertPool) {
	t.Helper()
	cert, pool := selfSigned(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln, tlsCfg: &tls.Config{Certificates: []tls.Certificate{cert}}, starttls: starttls}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f, pool
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(conn net.Conn) {
	defer func() { conn.Close() }()
	isTLS := false
	r := bufio.NewReader(conn)
	w := func(s string) { io.WriteString(conn, s+"\r\n") }
	w("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		f.mu.Lock()
		if verb == "AUTH" {
			f.cmds = append(f.cmds, "AUTH")
		} else {
			f.cmds = append(f.cmds, line)
		}
		f.mu.Unlock()
		switch verb {
		case "EHLO", "HELO":
			w("250-fake")
			if f.starttls && !isTLS {
				w("250-STARTTLS")
			}
			if isTLS {
				w("250-AUTH PLAIN")
			}
			w("250 8BITMIME")
		case "STARTTLS":
			w("220 go ahead")
			tc := tls.Server(conn, f.tlsCfg)
			if tc.Handshake() != nil {
				return
			}
			conn, r, isTLS = tc, bufio.NewReader(tc), true
		case "AUTH":
			parts := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(parts[len(parts)-1])
			fields := strings.Split(string(raw), "\x00")
			f.mu.Lock()
			if len(fields) == 3 {
				f.authUser, f.authPass = fields[1], fields[2]
			}
			f.authTLS = isTLS
			f.mu.Unlock()
			w("235 ok")
		case "MAIL":
			f.mu.Lock()
			f.from = line
			f.mu.Unlock()
			w("250 ok")
		case "RCPT":
			f.mu.Lock()
			f.to = line
			f.mu.Unlock()
			if f.rcptReply != "" {
				w(f.rcptReply)
			} else {
				w("250 ok")
			}
		case "DATA":
			w("354 go")
			var buf bytes.Buffer
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				buf.WriteString(strings.TrimPrefix(l, "."))
			}
			f.mu.Lock()
			f.data = buf.Bytes()
			f.mu.Unlock()
			w("250 queued")
		case "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func newSMTP(t *testing.T, f *fakeSMTP, pool *x509.CertPool) *SMTP {
	t.Helper()
	s, err := New(Config{
		Host: "127.0.0.1", Port: f.port(), User: "relay-user", Password: "s3cret",
		From: "REEL <noreply@reel.example>", TLS: &tls.Config{RootCAs: pool}, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC) }
	return s
}

func TestSendOverSTARTTLS(t *testing.T) {
	f, pool := startFake(t, true)
	s := newSMTP(t, f, pool)
	err := s.Send(context.Background(), Message{
		To: "fan@example.com", Subject: "你关注的演员有 2 部新作",
		Text:            "第一行\n第二行 https://reel.example/releases/abc-123",
		HTML:            "<p>新作 <a href=\"https://reel.example/releases/abc-123\">abc</a></p>",
		ListUnsubscribe: "https://reel.example/settings/notifications",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.authTLS || f.authUser != "relay-user" || f.authPass != "s3cret" {
		t.Fatalf("auth user=%q tls=%v", f.authUser, f.authTLS)
	}
	if f.from != "MAIL FROM:<noreply@reel.example> BODY=8BITMIME" && f.from != "MAIL FROM:<noreply@reel.example>" {
		t.Errorf("MAIL = %q", f.from)
	}
	if f.to != "RCPT TO:<fan@example.com>" {
		t.Errorf("RCPT = %q", f.to)
	}
	msg, err := mail.ReadMessage(bytes.NewReader(f.data))
	if err != nil {
		t.Fatal(err)
	}
	dec := new(mime.WordDecoder)
	subj, _ := dec.DecodeHeader(msg.Header.Get("Subject"))
	if subj != "你关注的演员有 2 部新作" {
		t.Errorf("Subject = %q", subj)
	}
	for k, want := range map[string]string{
		"List-Unsubscribe": "<https://reel.example/settings/notifications>",
		"To":               "<fan@example.com>",
		"From":             `"REEL" <noreply@reel.example>`,
		"MIME-Version":     "1.0",
		"Auto-Submitted":   "auto-generated",
	} {
		if got := msg.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.HasSuffix(msg.Header.Get("Message-ID"), "@reel.example>") {
		t.Errorf("Message-ID = %q", msg.Header.Get("Message-ID"))
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" {
		t.Fatalf("Content-Type = %q", msg.Header.Get("Content-Type"))
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var types, bodies []string
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Errorf("part CTE = %q", p.Header.Get("Content-Transfer-Encoding"))
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		types = append(types, p.Header.Get("Content-Type"))
		bodies = append(bodies, string(b))
	}
	if len(types) != 2 || types[0] != "text/plain; charset=utf-8" || types[1] != "text/html; charset=utf-8" {
		t.Fatalf("parts = %v", types)
	}
	if bodies[0] != "第一行\r\n第二行 https://reel.example/releases/abc-123" {
		t.Errorf("text part = %q", bodies[0])
	}
	if !strings.Contains(bodies[1], `<a href="https://reel.example/releases/abc-123">`) {
		t.Errorf("html part = %q", bodies[1])
	}
}

func TestRefusesWithoutSTARTTLS(t *testing.T) {
	f, pool := startFake(t, false)
	s := newSMTP(t, f, pool)
	err := s.Send(context.Background(), Message{To: "fan@example.com", Subject: "x", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v", err)
	}
	if IsPermanent(err) {
		t.Error("missing STARTTLS is a config problem, not a permanent recipient failure")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cmds {
		if c == "AUTH" || strings.HasPrefix(c, "MAIL") {
			t.Fatalf("sent %q in plaintext", c)
		}
	}
}

func TestPermanentAndTransientErrors(t *testing.T) {
	f, pool := startFake(t, true)
	s := newSMTP(t, f, pool)
	f.rcptReply = "550 no such user"
	err := s.Send(context.Background(), Message{To: "gone@example.com", Subject: "x", Text: "x"})
	if !IsPermanent(err) {
		t.Fatalf("550 must be permanent: %v", err)
	}
	f.rcptReply = "451 try later"
	err = s.Send(context.Background(), Message{To: "busy@example.com", Subject: "x", Text: "x"})
	if err == nil || IsPermanent(err) {
		t.Fatalf("451 must be transient: %v", err)
	}
	if err := s.Send(context.Background(), Message{To: "not an address", Subject: "x", Text: "x"}); !IsPermanent(err) {
		t.Fatalf("bad recipient must be permanent: %v", err)
	}
	// Untrusted certificate: TLS fails, nothing is sent.
	bad := newSMTP(t, f, x509.NewCertPool())
	if err := bad.Send(context.Background(), Message{To: "a@example.com", Subject: "x", Text: "x"}); err == nil {
		t.Fatal("untrusted certificate must fail")
	}
}

func TestBuildRejectsHeaderInjection(t *testing.T) {
	from, to := &mail.Address{Address: "a@b.c"}, &mail.Address{Address: "d@e.f"}
	if _, err := Build(Message{Subject: "x\r\nBcc: evil@x", Text: "x"}, from, to, time.Now(), "<1@b.c>"); err == nil {
		t.Error("subject with CRLF must fail")
	}
	b, err := Build(Message{Subject: "plain", Text: "only text"}, from, to, time.Now(), "<1@b.c>")
	if err != nil || !strings.Contains(string(b), "Content-Type: text/plain; charset=utf-8") {
		t.Errorf("text-only build: %v\n%s", err, b)
	}
}

func TestNewValidates(t *testing.T) {
	for _, c := range []Config{
		{Port: 587, From: "a@b.c"},
		{Host: "h", Port: 0, From: "a@b.c"},
		{Host: "h", Port: 587, From: "nope"},
		{Host: "h", Port: 587, From: "a@b.c", User: "u"},
	} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) succeeded", c)
		}
	}
	if _, err := New(Config{Host: "h", Port: 587, From: "a@b.c"}); err != nil {
		t.Error(err)
	}
}
