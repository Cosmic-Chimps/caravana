// Package smtptest is a small fake mail server for tests of email channels
// (notify, service, ui and the lab's steps). It speaks just enough ESMTP for
// net/smtp's client: greeting, EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA,
// RSET, NOOP and QUIT, and records every message it accepts.
//
// It is a normal package, not a _test file, so other packages' tests can import
// it; the binary never does.
package smtptest

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// Options say how the server behaves. The zero value is a plain server that
// accepts everything, on 127.0.0.1.
type Options struct {
	// ImplicitTLS starts TLS on connect (the "tls" security, port 465).
	ImplicitTLS bool
	// StartTLS offers the STARTTLS extension on a plain connection.
	StartTLS bool
	// AuthUser and AuthPassword turn AUTH PLAIN on and require it before MAIL:
	// a client that doesn't log in with them is refused. AUTH is offered only
	// after TLS began, unless AllowPlainAuth is set.
	AuthUser, AuthPassword string
	AllowPlainAuth         bool
	// RefuseRcpt, when not empty, makes RCPT TO answer 550 for an address that
	// contains it.
	RefuseRcpt string
	// Hang accepts connections and never answers, for timeout tests.
	Hang bool
}

// Message is one message the server accepted.
type Message struct {
	From     string
	To       []string
	Data     []byte // the message as sent, headers and body, with CRLF line ends
	TLS      bool   // the connection was encrypted when it was sent
	AuthUser string // who logged in, or ""
}

// Server is a running fake mail server.
type Server struct {
	opt   Options
	ln    net.Listener
	cert  tls.Certificate
	roots *x509.CertPool

	mu       sync.Mutex
	messages []Message
	conns    []net.Conn
}

// Start runs a server on 127.0.0.1 with a free port and stops it when the test
// ends.
func Start(t testing.TB, opt Options) *Server {
	t.Helper()
	cert, roots, err := selfSigned()
	if err != nil {
		t.Fatalf("smtptest: make a certificate: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("smtptest: listen: %v", err)
	}
	s := &Server{opt: opt, ln: ln, cert: cert, roots: roots}
	go s.serve()
	t.Cleanup(s.Close)
	return s
}

// Host and Port are where to connect.
func (s *Server) Host() string { return "127.0.0.1" }
func (s *Server) Port() int    { return s.ln.Addr().(*net.TCPAddr).Port }

// RootCAs trusts the server's certificate, for a client that checks it.
func (s *Server) RootCAs() *x509.CertPool { return s.roots }

// Close stops the server and drops its connections.
func (s *Server) Close() {
	s.ln.Close()
	s.mu.Lock()
	for _, c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
}

// Messages returns what the server accepted so far, oldest first.
func (s *Server) Messages() []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.messages...)
}

// WaitFor waits up to timeout for a message that match accepts, and returns it.
func (s *Server) WaitFor(match func(Message) bool, timeout time.Duration) (Message, bool) {
	deadline := time.Now().Add(timeout)
	for {
		for _, m := range s.Messages() {
			if match(m) {
				return m, true
			}
		}
		if time.Now().After(deadline) {
			return Message{}, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *Server) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns = append(s.conns, conn)
		s.mu.Unlock()
		go s.handle(conn)
	}
}

func (s *Server) tlsConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// handle is one connection: a loop of commands and replies.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	if s.opt.Hang {
		buf := make([]byte, 1)
		conn.Read(buf) // until the client gives up and closes
		return
	}

	encrypted := false
	if s.opt.ImplicitTLS {
		tc := tls.Server(conn, s.tlsConfig())
		if err := tc.Handshake(); err != nil {
			return
		}
		conn, encrypted = tc, true
	}
	r := bufio.NewReader(conn)
	reply := func(format string, args ...any) { fmt.Fprintf(conn, format+"\r\n", args...) }

	var from, authUser string
	var to []string
	reset := func() { from, to = "", nil }

	reply("220 smtptest ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb, rest, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			lines := []string{"smtptest", "8BITMIME"}
			if s.opt.StartTLS && !encrypted {
				lines = append(lines, "STARTTLS")
			}
			if s.opt.AuthUser != "" && (encrypted || s.opt.AllowPlainAuth) {
				lines = append(lines, "AUTH PLAIN")
			}
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				reply("250%s%s", sep, l)
			}
		case "HELO":
			reply("250 smtptest")
		case "STARTTLS":
			if !s.opt.StartTLS || encrypted {
				reply("503 STARTTLS not available")
				continue
			}
			reply("220 go ahead")
			tc := tls.Server(conn, s.tlsConfig())
			if err := tc.Handshake(); err != nil {
				return
			}
			conn, encrypted = tc, true
			r = bufio.NewReader(conn)
			reset()
		case "AUTH":
			if s.opt.AuthUser == "" || !(encrypted || s.opt.AllowPlainAuth) {
				reply("503 AUTH not available")
				continue
			}
			mech, initial, _ := strings.Cut(rest, " ")
			if !strings.EqualFold(mech, "PLAIN") {
				reply("504 only PLAIN")
				continue
			}
			if initial == "" {
				reply("334 ")
				if initial, err = r.ReadString('\n'); err != nil {
					return
				}
				initial = strings.TrimSpace(initial)
			}
			raw, err := base64.StdEncoding.DecodeString(initial)
			parts := strings.Split(string(raw), "\x00")
			if err != nil || len(parts) != 3 || parts[1] != s.opt.AuthUser || parts[2] != s.opt.AuthPassword {
				reply("535 authentication failed")
				continue
			}
			authUser = parts[1]
			reply("235 welcome")
		case "MAIL":
			if s.opt.AuthUser != "" && authUser == "" {
				reply("530 authentication required")
				continue
			}
			from = address(rest)
			reply("250 ok")
		case "RCPT":
			addr := address(rest)
			if s.opt.RefuseRcpt != "" && strings.Contains(addr, s.opt.RefuseRcpt) {
				reply("550 no such user here")
				continue
			}
			to = append(to, addr)
			reply("250 ok")
		case "DATA":
			if from == "" || len(to) == 0 {
				reply("503 need MAIL and RCPT first")
				continue
			}
			reply("354 end with a line holding a dot")
			var data strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(strings.TrimPrefix(l, ".")) // undo the dot stuffing
			}
			s.mu.Lock()
			s.messages = append(s.messages, Message{From: from, To: to, Data: []byte(data.String()), TLS: encrypted, AuthUser: authUser})
			s.mu.Unlock()
			reset()
			reply("250 queued")
		case "RSET":
			reset()
			reply("250 ok")
		case "NOOP":
			reply("250 ok")
		case "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

// address returns what is between "<" and ">" in "FROM:<a@b> BODY=8BITMIME".
func address(s string) string {
	start, end := strings.Index(s, "<"), strings.Index(s, ">")
	if start < 0 || end < start {
		return ""
	}
	return s[start+1 : end]
}

// selfSigned makes an ECDSA certificate for 127.0.0.1 and localhost that signs
// itself, and a pool that trusts it.
func selfSigned() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "smtptest"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool, nil
}
