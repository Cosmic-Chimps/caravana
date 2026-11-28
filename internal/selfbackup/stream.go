package selfbackup

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
)

// The body is cut in chunks of chunkSize bytes, each sealed with AES-256-GCM. The nonce of a chunk is its
// number (11 bytes, big-endian) and one byte that is 1 on the last chunk and 0 on every other, as age's
// STREAM does: a file cut short, a chunk moved and a chunk added all fail to open. The last chunk is the
// only one that may be shorter than chunkSize, and a body of no bytes is one empty last chunk.
const (
	chunkSize = 64 << 10
	nonceSize = 12
	tagSize   = 16
)

// ErrCorrupt means the body didn't open: the file is damaged, cut short, or not made for this key.
var ErrCorrupt = errors.New("the backup is damaged or cut short, or it was made for another key")

func nonceFor(counter uint64, last bool) []byte {
	n := make([]byte, nonceSize)
	for i := 0; i < 8; i++ {
		n[10-i] = byte(counter >> (8 * i))
	}
	if last {
		n[11] = 1
	}
	return n
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealWriter encrypts what is written to it into w. Close must be called: it seals the last chunk.
type sealWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	buf     []byte
	counter uint64
	err     error
}

func newSealWriter(w io.Writer, key []byte) (*sealWriter, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &sealWriter{w: w, aead: aead, buf: make([]byte, 0, chunkSize)}, nil
}

func (s *sealWriter) Write(p []byte) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	n := len(p)
	for len(p) > 0 {
		// A full buffer is sealed only when more arrives: the last chunk is whatever is left at Close.
		if len(s.buf) == chunkSize {
			if err := s.seal(false); err != nil {
				return 0, err
			}
		}
		take := min(chunkSize-len(s.buf), len(p))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
	}
	return n, nil
}

func (s *sealWriter) seal(last bool) error {
	out := s.aead.Seal(nil, nonceFor(s.counter, last), s.buf, nil)
	s.counter++
	s.buf = s.buf[:0]
	if _, err := s.w.Write(out); err != nil {
		s.err = err
		return err
	}
	return nil
}

// Close seals the last chunk. It doesn't close w.
func (s *sealWriter) Close() error {
	if s.err != nil {
		return s.err
	}
	err := s.seal(true)
	if err == nil {
		s.err = errors.New("write after close")
	}
	return err
}

// openReader decrypts what sealWriter wrote.
type openReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	counter uint64
	plain   []byte // decrypted bytes not yet read
	done    bool
	err     error
}

func newOpenReader(r io.Reader, key []byte) (*openReader, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	return &openReader{r: bufio.NewReaderSize(r, chunkSize+tagSize+1), aead: aead}, nil
}

func (o *openReader) Read(p []byte) (int, error) {
	for len(o.plain) == 0 {
		if o.err != nil {
			return 0, o.err
		}
		if o.done {
			return 0, io.EOF
		}
		o.next()
	}
	n := copy(p, o.plain)
	o.plain = o.plain[n:]
	return n, nil
}

// next reads and opens one chunk. A chunk is the last when nothing follows it.
func (o *openReader) next() {
	ct := make([]byte, chunkSize+tagSize)
	n, err := io.ReadFull(o.r, ct)
	switch {
	case err == nil:
		// A full chunk: it is the last one only if the file ends here.
		if _, perr := o.r.Peek(1); perr == io.EOF {
			o.open(ct[:n], true)
		} else if perr != nil {
			o.err = fmt.Errorf("read the backup: %w", perr)
		} else {
			o.open(ct[:n], false)
		}
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		o.open(ct[:n], true)
	default:
		o.err = fmt.Errorf("read the backup: %w", err)
	}
}

func (o *openReader) open(ct []byte, last bool) {
	if len(ct) < tagSize {
		o.err = ErrCorrupt
		return
	}
	plain, err := o.aead.Open(nil, nonceFor(o.counter, last), ct, nil)
	if err != nil {
		o.err = ErrCorrupt
		return
	}
	o.counter++
	o.plain = plain
	if last {
		o.done = true
	}
}
