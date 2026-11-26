package app

import (
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits of a service's files (feature 060).
const (
	// MaxFiles is how many files one service may mount.
	MaxFiles = 16
	// MaxFilePath is the longest path of a file, in bytes.
	MaxFilePath = 1024
	// MaxFileContent is the largest plain content of one file, in bytes.
	MaxFileContent = 64 << 10
	// MaxAppFileContent is the largest plain content of all the files of an app, in bytes.
	MaxAppFileContent = 256 << 10
	// DefaultFileMode is the mode of a file that doesn't name one.
	DefaultFileMode = "0644"
)

// fileModeRE is an octal mode: three digits, with an optional leading zero.
var fileModeRE = regexp.MustCompile(`^0?[0-7]{3}$`)

// reservedFileDirs are folders a file may not be put in or at: the kernel's, the devices and Nomad's own
// directories in a container.
var reservedFileDirs = []string{"/proc", "/sys", "/dev", "/alloc", "/local", "/secrets"}

// reservedFilePaths are files Docker or Caravana writes in every container.
var reservedFilePaths = []string{"/etc/hosts", "/etc/hostname", "/etc/resolv.conf"}

// File is a file mounted read-only at Path in every copy of a service: a config file whose content is in
// the app, or a file whose content is one of the service's secrets (a certificate's key). Exactly one of
// Content and Secret is set.
type File struct {
	// Path is where the file is in the container: absolute and clean.
	Path string `json:"path"`
	// Content is the file's text. It is part of the stored app: not for anything secret.
	Content string `json:"content,omitempty"`
	// Secret names one of the service's own secrets, whose value is the file's content. A secret a file
	// names is written to the file and not to the environment; it keeps every other secret behavior
	// (write-only, a provider reference or a template, kept when unnamed).
	Secret string `json:"secret,omitempty"`
	// Mode is the file's permissions in octal, "0640". Empty means DefaultFileMode.
	Mode string `json:"mode,omitempty"`
	// UID and GID own the file. nil means Nomad's default (root).
	UID *int `json:"uid,omitempty"`
	GID *int `json:"gid,omitempty"`
}

// Perms is the mode as Nomad's template takes it: four octal digits.
func (f File) Perms() string {
	switch {
	case f.Mode == "":
		return DefaultFileMode
	case len(f.Mode) == 3:
		return "0" + f.Mode
	}
	return f.Mode
}

// FileSecrets lists the names of the secrets the service's files hold, each once, in the order the files
// name them. Those secrets go to their files and not to the environment.
func (s Service) FileSecrets() []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range s.Files {
		if f.Secret != "" && !seen[f.Secret] {
			seen[f.Secret] = true
			out = append(out, f.Secret)
		}
	}
	return out
}

// CheckFile checks one file. field is its path ("services[0].files[1]"); an error names field.<part>.
// secrets are the names of the service's secrets (nil when the caller can't know them, which skips that
// check) and mounts the paths its volumes are mounted at.
func CheckFile(field string, f File, secrets map[string]bool, mounts map[string]bool) error {
	if err := CheckFilePath(field+".path", f.Path); err != nil {
		return err
	}
	if mounts[f.Path] {
		return NewFieldError(field+".path", "%s is a volume's mount path: a file goes in a new path or inside the volume", f.Path)
	}
	switch {
	case f.Content != "" && f.Secret != "":
		return NewFieldError(field, "give content or secret, not both")
	case f.Content == "" && f.Secret == "":
		return NewFieldError(field, "give content or secret")
	}
	if f.Secret != "" && secrets != nil && !secrets[f.Secret] {
		return NewFieldError(field+".secret", "%q isn't one of the service's secrets", f.Secret)
	}
	if err := CheckFileContent(field+".content", f.Content); err != nil {
		return err
	}
	if f.Mode != "" && !fileModeRE.MatchString(f.Mode) {
		return NewFieldError(field+".mode", "%q must be an octal mode such as 0644 or 640", f.Mode)
	}
	if f.UID != nil && (*f.UID < 0 || *f.UID > 65535) {
		return NewFieldError(field+".uid", "is %d; it is between 0 and 65535", *f.UID)
	}
	if f.GID != nil && (*f.GID < 0 || *f.GID > 65535) {
		return NewFieldError(field+".gid", "is %d; it is between 0 and 65535", *f.GID)
	}
	return nil
}

// CheckFileContent checks a file's text; field is the path an error names.
func CheckFileContent(field, content string) error {
	switch {
	case len(content) > MaxFileContent:
		return NewFieldError(field, "is %d bytes; at most %d are allowed", len(content), MaxFileContent)
	case !utf8.ValidString(content):
		return NewFieldError(field, "must be valid UTF-8")
	case strings.ContainsRune(content, 0):
		return NewFieldError(field, "must not contain a NUL byte")
	}
	return nil
}

// CheckFilePath checks where a file is put; field is the path an error names.
func CheckFilePath(field, p string) error {
	switch {
	case p == "":
		return NewFieldError(field, "is required")
	case len(p) > MaxFilePath:
		return NewFieldError(field, "is %d bytes; at most %d are allowed", len(p), MaxFilePath)
	case strings.ContainsFunc(p, func(r rune) bool { return !unicode.IsPrint(r) || unicode.IsSpace(r) }):
		return NewFieldError(field, "%q must be printable and without spaces", p)
	case !strings.HasPrefix(p, "/"):
		return NewFieldError(field, "%q must be an absolute path", p)
	case p == "/" || path.Clean(p) != p:
		return NewFieldError(field, "%q must be a clean path to a file (no trailing /, //, . or ..)", p)
	}
	for _, dir := range reservedFileDirs {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			return NewFieldError(field, "%s is in %s, which the container's runtime owns", p, dir)
		}
	}
	for _, r := range reservedFilePaths {
		if p == r {
			return NewFieldError(field, "%s is written by Caravana for every container", p)
		}
	}
	return nil
}

// validateFiles checks a service's files. field is the service's path, e.g. "services[0]". total is the
// plain content the app's earlier services hold; it returns the total with this service's.
func validateFiles(field string, svc Service, total int) (int, error) {
	if len(svc.Files) > MaxFiles {
		return total, NewFieldError(field+".files", "has %d files; at most %d are allowed", len(svc.Files), MaxFiles)
	}
	secrets := map[string]bool{}
	for _, s := range svc.Secrets {
		secrets[s.Name] = true
	}
	mounts := map[string]bool{}
	for _, v := range svc.Volumes {
		mounts[v.Path] = true
	}
	seen := map[string]bool{}
	var earlier []string
	for k, f := range svc.Files {
		ff := fmt.Sprintf("%s.files[%d]", field, k)
		if err := CheckFile(ff, f, secrets, mounts); err != nil {
			return total, err
		}
		if seen[f.Path] {
			return total, NewFieldError(ff+".path", "%s is used by an earlier file", f.Path)
		}
		seen[f.Path] = true
		// A file is mounted at its path, and Docker can't mount a second target inside a file (nor a file
		// over a folder that holds another one's target): the copy would not start. Whole segments only.
		for _, p := range earlier {
			switch {
			case strings.HasPrefix(f.Path, p+"/"):
				return total, NewFieldError(ff+".path", "%s is inside %s, which is a file", f.Path, p)
			case strings.HasPrefix(p, f.Path+"/"):
				return total, NewFieldError(ff+".path", "%s is a folder of %s, another file: a file can't be both", f.Path, p)
			}
		}
		earlier = append(earlier, f.Path)
		total += len(f.Content)
		if total > MaxAppFileContent {
			return total, NewFieldError(ff+".content", "the app's files hold more than %d bytes of content", MaxAppFileContent)
		}
	}
	return total, nil
}
