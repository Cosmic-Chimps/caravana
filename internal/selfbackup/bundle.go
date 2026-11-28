package selfbackup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// The backup file (plan D2): a plain header, then the body, which is gzip(tar) of the database, the
// variables and a manifest, encrypted in chunks (stream.go).
//
//	CARAVANA-BACKUP 1\n
//	{"createdAt":…,"version":…,"schema":…,"keyFingerprint":…,"ephemeralKey":…}\n
//	<encrypted body>
//
// The header is readable without the key, so a listing says which Caravana and which key made each
// backup. It is also part of what the body's key is derived from, so a changed header can't be opened.
const (
	magic = "CARAVANA-BACKUP 1"
	// HeaderMax is how many bytes a reader needs from the start of a file to read its header.
	HeaderMax = 4096
	hkdfInfo  = "caravana self-backup v1"
)

// The files a backup holds, by name in its tar.
const (
	DatabaseFile  = "caravana.db"
	VariablesFile = "variables.json"
	ManifestFile  = "manifest.json"
)

// Variable is a Nomad Variable in a backup.
type Variable struct {
	Path  string            `json:"path"`
	Items map[string]string `json:"items"`
}

// Manifest counts what a backup holds, for a listing and for the restore's last line.
type Manifest struct {
	Apps      int `json:"apps"`
	People    int `json:"people"`
	Variables int `json:"variables"`
}

// Meta is what the header says about a backup.
type Meta struct {
	CreatedAt      time.Time
	Version        string // the Caravana that made it, "" for a development build
	Schema         int    // the newest migration of the database in it
	KeyFingerprint string // of the recovery key it is encrypted to
}

// Header is a backup's header as read.
type Header struct {
	Meta
	ephemeral []byte // the ephemeral public key
	raw       []byte // the header's two lines, exactly as written
}

type headerJSON struct {
	CreatedAt      string `json:"createdAt"`
	Version        string `json:"version"`
	Schema         int    `json:"schema"`
	KeyFingerprint string `json:"keyFingerprint"`
	EphemeralKey   string `json:"ephemeralKey"`
}

// File is one file to put in a backup: Size bytes read from Body.
type File struct {
	Name string
	Size int64
	Body io.Reader
}

// Write writes a backup of files to w, encrypted to the public key recipient (meta.KeyFingerprint must
// be its fingerprint, and is checked). random is crypto/rand.Reader in production.
func Write(w io.Writer, random io.Reader, recipient []byte, meta Meta, files []File) error {
	pub, err := ecdh.X25519().NewPublicKey(recipient)
	if err != nil {
		return fmt.Errorf("write a backup: the recovery key's public half: %w", err)
	}
	if meta.KeyFingerprint != Fingerprint(recipient) {
		return errors.New("write a backup: the header names another key than the one it is encrypted to")
	}
	seed := make([]byte, 32)
	if _, err := io.ReadFull(random, seed); err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	eph, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	hdr, err := json.Marshal(headerJSON{CreatedAt: meta.CreatedAt.UTC().Format(time.RFC3339), Version: meta.Version,
		Schema: meta.Schema, KeyFingerprint: meta.KeyFingerprint, EphemeralKey: base64.StdEncoding.EncodeToString(eph.PublicKey().Bytes())})
	if err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	raw := []byte(magic + "\n" + string(hdr) + "\n")
	key, err := bodyKey(eph.ECDH, pub, eph.PublicKey().Bytes(), recipient, raw)
	if err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	if _, err := w.Write(raw); err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	sealed, err := newSealWriter(w, key)
	if err != nil {
		return fmt.Errorf("write a backup: %w", err)
	}
	gz := gzip.NewWriter(sealed)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.Name, Mode: 0o600, Size: f.Size, ModTime: meta.CreatedAt.UTC(), Typeflag: tar.TypeReg}); err != nil {
			return fmt.Errorf("write a backup: %s: %w", f.Name, err)
		}
		if n, err := io.Copy(tw, f.Body); err != nil || n != f.Size {
			if err == nil {
				err = fmt.Errorf("read %d bytes, expected %d", n, f.Size)
			}
			return fmt.Errorf("write a backup: %s: %w", f.Name, err)
		}
	}
	for _, c := range []interface{ Close() error }{tw, gz, sealed} {
		if err := c.Close(); err != nil {
			return fmt.Errorf("write a backup: %w", err)
		}
	}
	return nil
}

// bodyKey derives the AES key: HKDF-SHA256 over the shared secret, salted with both public keys and bound
// to the header's bytes.
func bodyKey(ecdhFn func(*ecdh.PublicKey) ([]byte, error), peer *ecdh.PublicKey, ephemeralPub, recipientPub, rawHeader []byte) ([]byte, error) {
	shared, err := ecdhFn(peer)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(rawHeader)
	salt := append(append([]byte{}, ephemeralPub...), recipientPub...)
	return hkdf.Key(sha256.New, shared, salt, hkdfInfo+string(sum[:]), 32)
}

// ErrNotABackup means the file doesn't start like one of ours.
var ErrNotABackup = errors.New("this isn't a Caravana backup")

// ReadHeader reads the header from the start of a backup (HeaderMax bytes are enough).
func ReadHeader(r io.Reader) (Header, error) {
	return readHeaderFrom(bufio.NewReaderSize(r, HeaderMax))
}

// parseHeader reads the header's two lines, exactly as they are in the file.
func parseHeader(raw []byte) (Header, error) {
	first, second, ok := strings.Cut(string(raw), "\n")
	if !ok || first != magic {
		return Header{}, ErrNotABackup
	}
	var j headerJSON
	if err := json.Unmarshal([]byte(strings.TrimSuffix(second, "\n")), &j); err != nil {
		return Header{}, ErrNotABackup
	}
	created, err := time.Parse(time.RFC3339, j.CreatedAt)
	if err != nil {
		return Header{}, ErrNotABackup
	}
	eph, err := base64.StdEncoding.DecodeString(j.EphemeralKey)
	if err != nil || len(eph) != 32 {
		return Header{}, ErrNotABackup
	}
	return Header{Meta: Meta{CreatedAt: created.UTC(), Version: j.Version, Schema: j.Schema, KeyFingerprint: j.KeyFingerprint},
		ephemeral: eph, raw: raw}, nil
}

// KeyMismatchError is a backup made for another recovery key.
type KeyMismatchError struct {
	Backup, Key string // the fingerprints
}

func (e *KeyMismatchError) Error() string {
	return "this backup was made for the recovery key with fingerprint " + e.Backup + ", not the one given (" + e.Key + ")"
}

// Read opens a backup with key and calls visit for each file in it, in order. visit must read body to its
// end or return an error; the body is valid only during the call. Read returns the header. It reads the
// whole file, so a file that is cut short, changed or made for another key is an error even when every
// file was visited. A key that isn't the one the backup was made for is a *KeyMismatchError before
// anything is decrypted.
func Read(r io.Reader, key RecoveryKey, visit func(name string, size int64, body io.Reader) error) (Header, error) {
	br := bufio.NewReaderSize(r, chunkSize+tagSize+1)
	// The header is read from the same buffered reader the body follows.
	h, err := readHeaderFrom(br)
	if err != nil {
		return Header{}, err
	}
	if h.KeyFingerprint != key.Fingerprint() {
		return h, &KeyMismatchError{Backup: h.KeyFingerprint, Key: key.Fingerprint()}
	}
	eph, err := ecdh.X25519().NewPublicKey(h.ephemeral)
	if err != nil {
		return h, ErrNotABackup
	}
	aesKey, err := bodyKey(key.private.ECDH, eph, h.ephemeral, key.Public(), h.raw)
	if err != nil {
		return h, fmt.Errorf("open the backup: %w", err)
	}
	opened, err := newOpenReader(br, aesKey)
	if err != nil {
		return h, fmt.Errorf("open the backup: %w", err)
	}
	gz, err := gzip.NewReader(opened)
	if err != nil {
		return h, corrupt(err)
	}
	tr := tar.NewReader(gz)
	for {
		th, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return h, corrupt(err)
		}
		if th.Typeflag != tar.TypeReg || strings.Contains(th.Name, "/") || th.Name == "" {
			return h, fmt.Errorf("%w: it holds an entry named %q", ErrCorrupt, th.Name)
		}
		if err := visit(th.Name, th.Size, tr); err != nil {
			return h, err
		}
	}
	// Read to the end: the last chunk's seal and gzip's checksum are only checked there.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return h, corrupt(err)
	}
	if _, err := io.Copy(io.Discard, opened); err != nil {
		return h, corrupt(err)
	}
	return h, nil
}

// readHeaderFrom reads the two header lines from br without reading past them.
func readHeaderFrom(br *bufio.Reader) (Header, error) {
	var raw bytes.Buffer
	for range 2 {
		line, err := readLine(br, HeaderMax-raw.Len())
		if err != nil {
			return Header{}, ErrNotABackup
		}
		raw.Write(line)
	}
	return parseHeader(raw.Bytes())
}

func readLine(br *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for len(line) < limit {
		b, err := br.ReadByte()
		if err != nil {
			return nil, err
		}
		line = append(line, b)
		if b == '\n' {
			return line, nil
		}
	}
	return nil, ErrNotABackup
}

// corrupt says a stream error of the decrypted body is a damaged file, unless it already is one.
func corrupt(err error) error {
	if errors.Is(err, ErrCorrupt) {
		return err
	}
	return fmt.Errorf("%w (%v)", ErrCorrupt, err)
}

// ReadAll is Read for a backup that is read whole into memory by name: it returns the manifest, the
// variables and the database's bytes only when the caller asks (maxDB bounds the database it will hold;
// 0 skips it). It exists for tests and for small tools; Caravana's restore streams the database to disk.
func ReadAll(r io.Reader, key RecoveryKey, maxDB int64) (Header, Manifest, []Variable, []byte, error) {
	var m Manifest
	var vars []Variable
	var db []byte
	h, err := Read(r, key, func(name string, size int64, body io.Reader) error {
		switch name {
		case ManifestFile:
			return json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&m)
		case VariablesFile:
			return json.NewDecoder(io.LimitReader(body, 256<<20)).Decode(&vars)
		case DatabaseFile:
			if maxDB <= 0 {
				_, err := io.Copy(io.Discard, body)
				return err
			}
			if size > maxDB {
				return fmt.Errorf("the database in the backup is %s bytes, over the limit", strconv.FormatInt(size, 10))
			}
			var err error
			db, err = io.ReadAll(body)
			return err
		}
		_, err := io.Copy(io.Discard, body)
		return err
	})
	return h, m, vars, db, err
}
