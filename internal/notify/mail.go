package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// mailFooter ends every message: who sent it and where the owner changes what
// an address receives.
const mailFooter = "Sent by Caravana. Choose what this address receives in Settings › Notifications."

// Mail is a rendered email: the envelope and the whole message, headers and
// body, with CRLF line ends.
type Mail struct {
	From string
	To   []string
	Data []byte
}

// RenderMail builds the message that delivers ev to an email channel. Like
// Render it is pure: the Date and Message-ID headers and the multipart
// boundary come from now and deliveryID, so the same input gives the same
// message.
//
// The message has a plain-text part and a minimal HTML part, both quoted-
// printable UTF-8. The title is the subject; the parts carry the rest.
func RenderMail(ch Channel, ev Event, deliveryID string, now time.Time) (Mail, error) {
	if ch.Kind != KindEmail || ch.SMTP == nil {
		return Mail{}, errors.New("render an email message: the channel has no mail settings")
	}
	from, err := parseMailAddress(ch.SMTP.From)
	if err != nil {
		return Mail{}, fmt.Errorf("render an email message: the from address: %w", err)
	}
	if len(ch.SMTP.To) == 0 {
		return Mail{}, errors.New("render an email message: the channel has no recipient")
	}
	var to, toHeader []string
	for _, raw := range ch.SMTP.To {
		a, err := parseMailAddress(raw)
		if err != nil {
			return Mail{}, fmt.Errorf("render an email message: a to address: %w", err)
		}
		to = append(to, a.Address)
		toHeader = append(toHeader, a.String())
	}

	id := mailToken(deliveryID)
	boundary := "caravana-" + id
	subject := []rune(oneLine(ev.Title))
	if len(subject) > 250 { // a header line has to stay short
		subject = append(subject[:249], '…')
	}

	var msg bytes.Buffer
	header := func(name, value string) { msg.WriteString(name + ": " + value + "\r\n") }
	header("From", from.String())
	header("To", strings.Join(toHeader, ", "))
	// An encoded subject is a run of encoded words of at most 75 characters each;
	// putting each on a line of its own keeps the header under SMTP's 998-character
	// line limit even for 250 characters that all need encoding.
	header("Subject", strings.ReplaceAll(mime.QEncoding.Encode("utf-8", string(subject)), " =?", "\r\n =?"))
	header("Date", now.UTC().Format(time.RFC1123Z))
	header("Message-ID", "<"+id+"@caravana>")
	header("MIME-Version", "1.0")
	header("Auto-Submitted", "auto-generated")
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	msg.WriteString("\r\n")

	mw := multipart.NewWriter(&msg)
	if err := mw.SetBoundary(boundary); err != nil {
		return Mail{}, fmt.Errorf("render an email message: %w", err)
	}
	for _, part := range []struct{ typ, body string }{
		{"text/plain", mailText(ev)},
		{"text/html", mailHTML(ev)},
	} {
		w, err := mw.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.typ + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return Mail{}, fmt.Errorf("render an email message: %w", err)
		}
		qp := quotedprintable.NewWriter(w)
		if _, err := qp.Write([]byte(part.body)); err != nil {
			return Mail{}, fmt.Errorf("render an email message: %w", err)
		}
		if err := qp.Close(); err != nil {
			return Mail{}, fmt.Errorf("render an email message: %w", err)
		}
	}
	if err := mw.Close(); err != nil {
		return Mail{}, fmt.Errorf("render an email message: %w", err)
	}
	return Mail{From: from.Address, To: to, Data: msg.Bytes()}, nil
}

// parseMailAddress parses one address, with or without a display name. A line
// break anywhere in it is refused before the parser sees it: it would end the
// header and let a value start another.
func parseMailAddress(raw string) (*mail.Address, error) {
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errors.New("holds a line break")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil {
		return nil, errors.New("is not an email address")
	}
	return a, nil
}

// mailToken keeps the letters, digits, "_" and "-" of s, so a delivery ID can
// be part of a Message-ID and a boundary whatever it holds.
func mailToken(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

// mailText is the plain-text part: the event's text, the link when there is
// one, and the footer behind a signature separator.
func mailText(ev Event) string {
	text := withDetail(ev)
	if ev.URL != "" {
		text += "\n\n" + ev.URL
	}
	return text + "\n\n-- \n" + mailFooter + "\n"
}

// mailHTML is the HTML part: the same words with no style and no images, every
// value escaped.
func mailHTML(ev Event) string {
	lines := strings.Split(withDetail(ev), "\n")
	for i, l := range lines {
		lines[i] = html.EscapeString(l)
	}
	var b strings.Builder
	b.WriteString("<p><strong>" + html.EscapeString(oneLine(ev.Title)) + "</strong></p>\n")
	b.WriteString("<p>" + strings.Join(lines, "<br>\n") + "</p>\n")
	if ev.URL != "" {
		u := html.EscapeString(oneLine(ev.URL))
		b.WriteString(`<p><a href="` + u + `">` + u + "</a></p>\n")
	}
	b.WriteString("<p>" + html.EscapeString(mailFooter) + "</p>\n")
	return b.String()
}

// SendMail delivers m through the mail server of ch and reports whether it
// accepted every recipient and the message. The whole conversation, from the
// connection to QUIT, has ten seconds. No error holds the password.
func SendMail(ctx context.Context, ch Channel, m Mail) error {
	return sendMail(ctx, ch, m, sendTimeout, nil)
}

// sendMail is SendMail with the timeout and the root certificates as
// parameters, so tests need not wait ten seconds or trust a real authority.
func sendMail(ctx context.Context, ch Channel, m Mail, timeout time.Duration, roots *x509.CertPool) (err error) {
	s := ch.SMTP
	if s == nil {
		return errors.New("send an email: the channel has no mail settings")
	}
	// Whatever goes wrong, the error is one short line without the password.
	defer func() {
		if err != nil {
			err = mailError(s.Host, err, ch.Secret)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return err
	}
	defer conn.Close()
	// One deadline for everything, and the connection closes if ctx ends first
	// (Caravana stopping), which unblocks whatever read is waiting.
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	defer context.AfterFunc(ctx, func() { conn.Close() })()

	tlsConfig := &tls.Config{ServerName: s.Host, RootCAs: roots, MinVersion: tls.VersionTLS12}
	if s.Security == SecurityTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return err
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return err
	}
	defer c.Close()

	if s.Security == SecuritySTARTTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the server doesn't offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.Username != "" {
		// PlainAuth refuses to send the password over a connection that isn't
		// encrypted, except to this host. That is the right rule.
		if err := c.Auth(smtp.PlainAuth("", s.Username, ch.Secret, s.Host)); err != nil {
			return err
		}
	}
	if err := c.Mail(m.From); err != nil {
		return err
	}
	for _, to := range m.To {
		if err := c.Rcpt(to); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(m.Data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// mailError words a failure of the conversation like Send's errors: the host
// named, one line, cut at errorBodyLimit, and the password, if it appears,
// blanked out.
func mailError(host string, err error, password string) error {
	msg := err.Error()
	if len(password) >= minScrub {
		msg = strings.ReplaceAll(msg, password, "…")
	}
	msg = strings.Join(strings.Fields(strings.ToValidUTF8(msg, "")), " ")
	if len(msg) > errorBodyLimit {
		msg = strings.ToValidUTF8(msg[:errorBodyLimit], "")
	}
	return fmt.Errorf("send to %s: %s", host, msg)
}

// minScrub is the shortest secret Deliver and SendMail blank out of an error:
// a shorter one would mangle every message it appears in by chance.
const minScrub = 6

// Deliver renders ev for ch and sends it: an email goes through RenderMail and
// SendMail, every other kind through Render and Send. It is the one path the
// sender and "Send a test" share.
//
// A Telegram bot token is part of the request's path, and a receiver may echo
// the token on its own, so an error never keeps ch.Secret, whatever the kind.
func Deliver(ctx context.Context, client *http.Client, ch Channel, ev Event, deliveryID string, now time.Time) error {
	var err error
	if ch.Kind == KindEmail {
		var m Mail
		if m, err = RenderMail(ch, ev, deliveryID, now); err == nil {
			err = SendMail(ctx, ch, m)
		}
	} else {
		var req *http.Request
		if req, err = Render(ch, ev, deliveryID, now); err == nil {
			err = Send(ctx, client, req)
		}
	}
	if err != nil && len(ch.Secret) >= minScrub && strings.Contains(err.Error(), ch.Secret) {
		err = errors.New(strings.ReplaceAll(err.Error(), ch.Secret, "…"))
	}
	return err
}
