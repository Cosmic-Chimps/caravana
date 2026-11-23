package app

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/registry"
)

// Build is where a service's image comes from when it is built from a Git
// repository on the host (feature 050). The service's Image is then the
// repository the build pushes to, with no tag or digest: every deploy builds
// the branch's newest commit, pushes it as <image>:git-<commit> and deploys the
// digest the push reported.
//
// The deploy key's private half and a token are not here: they live in a Nomad
// Variable (spec D3), so the stored app, its revision and every answer hold the
// source only.
type Build struct {
	// Repository is https://host/path[.git], ssh://user@host[:port]/path or
	// user@host:path. http:// is only for a loopback host. It holds no
	// credentials, query or fragment (see ParseRepository).
	Repository string `json:"repository,omitempty"`
	// Branch is the branch built. Empty means DefaultBuildBranch.
	Branch string `json:"branch,omitempty"`
	// Path is the folder of the repository that is the build context. Empty
	// means DefaultBuildPath, the root.
	Path string `json:"path,omitempty"`
	// Dockerfile is the Dockerfile's path relative to Path. Empty means
	// DefaultBuildDockerfile.
	Dockerfile string `json:"dockerfile,omitempty"`
	// Access is how the build reads the repository: BuildAccessPublic (or
	// empty), BuildAccessDeployKey (an SSH URL) or BuildAccessToken (an HTTPS
	// URL).
	Access string `json:"access,omitempty"`
	// HostKey is the SSH host's public key as a known_hosts line without the
	// host: "<type> <base64>". It is required for an SSH host other than the
	// ones in KnownGitHosts, and optional for those.
	HostKey string `json:"hostKey,omitempty"`
}

// How a build reads its repository (Build.Access).
const (
	BuildAccessPublic    = "public"
	BuildAccessDeployKey = "deploy-key"
	BuildAccessToken     = "token"
)

// What a Build leaves out means.
const (
	DefaultBuildBranch     = "main"
	DefaultBuildPath       = "."
	DefaultBuildDockerfile = "Dockerfile"
)

// ReservedVolumePrefix starts the names of Caravana's own host volumes of an
// app (the build cache, <app>_caravana-build). An app's volume may not take it.
const ReservedVolumePrefix = "caravana-"

// KnownGitHosts are the SSH hosts whose keys Caravana has built in
// (nomadjob.BuiltinKnownHosts holds them, and a test keeps the two lists in
// step). Any other SSH host needs Build.HostKey.
var KnownGitHosts = []string{"github.com", "gitlab.com"}

// Limits of a build's fields.
const (
	maxRepositoryBytes = 512
	maxBranchBytes     = 255
	maxBuildPathBytes  = 255
	maxHostKeyBytes    = 2048
)

// Defaults is b with every field the owner left out filled in. The deploy flow
// writes the result into the stored app, so what is stored says what runs.
func (b Build) Defaults() Build {
	if b.Branch == "" {
		b.Branch = DefaultBuildBranch
	}
	if b.Path == "" {
		b.Path = DefaultBuildPath
	}
	if b.Dockerfile == "" {
		b.Dockerfile = DefaultBuildDockerfile
	}
	if b.Access == "" {
		b.Access = BuildAccessPublic
	}
	return b
}

// BuiltService is the index of the app's built service. Validation allows one,
// so the first is the only one.
func (a App) BuiltService() (int, bool) {
	for i, s := range a.Services {
		if s.Build != nil {
			return i, true
		}
	}
	return 0, false
}

// Repository is a Git repository address read into the parts the build needs.
type Repository struct {
	// SSH is true for ssh:// and user@host:path, false for https:// and http://.
	SSH bool
	// Host is the host name or address in lower case, without brackets or a port.
	Host string
	// Port is the port the address names, 0 when it names none.
	Port int
	// Path is what follows the host, without the leading "/" of a URL.
	Path string
}

// Loopback reports whether the repository is served from the machine itself: a
// development server such as the lab's.
func (r Repository) Loopback() bool { return HostIsLoopback(r.Host) }

// HostIsLoopback reports whether host (with or without a port) is localhost or
// a loopback address.
func HostIsLoopback(host string) bool { return registry.IsLoopback(host) }

// HasBuiltinHostKeys reports whether Caravana has the SSH keys of host built in
// (KnownGitHosts). They are the keys of port 22; a host on another port needs
// its own HostKey.
func HasBuiltinHostKeys(host string, port int) bool {
	if port != 0 && port != 22 {
		return false
	}
	for _, h := range KnownGitHosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

var (
	// repositoryCharsRE is every character a repository address may have. It
	// leaves out spaces, quotes, "\", "$", "`", ";", "&", "|", "<", ">" and "*"
	// ("?" and "#" are refused first, with their own message), so an address is
	// the same text in a script, a known_hosts line and a log.
	repositoryCharsRE = regexp.MustCompile(`^[A-Za-z0-9._~:/@%+=,\[\]-]+$`)
	// scpRepositoryRE is user@host:path.
	scpRepositoryRE = regexp.MustCompile(`^([A-Za-z0-9._-]+)@([A-Za-z0-9.-]+):(.+)$`)
	// hostRE is a DNS name: letters, digits, "-" and ".", starting and ending
	// with a letter or digit.
	hostRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	// branchRE is a branch name's plain form.
	branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)
	// pathSegmentRE is one folder or file name of the build's path.
	pathSegmentRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// hostKeyRE is a known_hosts key: its type and its base64 body.
	hostKeyRE = regexp.MustCompile(`^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) ([A-Za-z0-9+/]+={0,2})$`)
)

// repositoryForms is what the repository's error sentences say is accepted.
const repositoryForms = "use https://host/path, ssh://user@host/path or user@host:path"

// ParseRepository reads a repository address. The error is a sentence for the
// field "services[i].build.repository": what is wrong and what is accepted.
func ParseRepository(s string) (Repository, error) {
	switch {
	case s == "":
		return Repository{}, fmt.Errorf("is required: %s", repositoryForms)
	case len(s) > maxRepositoryBytes:
		return Repository{}, fmt.Errorf("is %d bytes; at most %d are allowed", len(s), maxRepositoryBytes)
	case strings.ContainsAny(s, "?#"):
		return Repository{}, fmt.Errorf("must not have a query or a fragment")
	case !repositoryCharsRE.MatchString(s):
		return Repository{}, fmt.Errorf("must be plain ASCII without spaces, quotes or shell characters; %s", repositoryForms)
	}

	if !strings.Contains(s, "://") {
		m := scpRepositoryRE.FindStringSubmatch(s)
		if m == nil {
			return Repository{}, fmt.Errorf("%q is not an address; %s", s, repositoryForms)
		}
		if err := checkRepositoryHost(m[2]); err != nil {
			return Repository{}, err
		}
		return Repository{SSH: true, Host: strings.ToLower(m[2]), Path: m[3]}, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return Repository{}, fmt.Errorf("%q is not an address; %s", s, repositoryForms)
	}
	r := Repository{Host: strings.ToLower(u.Hostname()), Path: strings.TrimPrefix(u.Path, "/")}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Repository{}, fmt.Errorf("the port %q must be between 1 and 65535", p)
		}
		r.Port = n
	}
	if err := checkRepositoryHost(r.Host); err != nil {
		return Repository{}, err
	}
	if r.Path == "" {
		return Repository{}, fmt.Errorf("has no repository path after the host")
	}
	switch u.Scheme {
	case "https", "http":
		if u.User != nil {
			return Repository{}, fmt.Errorf("must not hold credentials: give a token as Git access instead")
		}
		if u.Scheme == "http" && !r.Loopback() {
			return Repository{}, fmt.Errorf("%q is plain http, which is only allowed for a host on this machine; use https://", s)
		}
	case "ssh":
		r.SSH = true
		if u.User == nil || u.User.Username() == "" {
			return Repository{}, fmt.Errorf("needs a user: ssh://git@host/path")
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return Repository{}, fmt.Errorf("must not hold a password: use a deploy key")
		}
	default:
		return Repository{}, fmt.Errorf("the scheme %q is not supported; %s", u.Scheme, repositoryForms)
	}
	return r, nil
}

// checkRepositoryHost accepts a DNS name or an IP address.
func checkRepositoryHost(host string) error {
	if host == "" {
		return fmt.Errorf("has no host; %s", repositoryForms)
	}
	if hostRE.MatchString(host) || net.ParseIP(host) != nil {
		return nil
	}
	return fmt.Errorf("the host %q is not a name or an address", host)
}

// validateBuild checks a service's build source: the repository, the names, how
// it is read and the host's key, and that the service's image is the
// repository to push to. What needs the installation (a stored key or token, a
// registry entry, the host's key) is checked by the deploy flow.
func validateBuild(field string, svc Service) error {
	b := svc.Build
	if b == nil {
		return nil
	}
	if imageHasTagOrDigest(svc.Image) {
		return NewFieldError(field+".image", "%q is where builds push to, so it must have no tag or digest: each build is pushed as %s:git-<commit>", svc.Image, svc.Image)
	}

	build := b.Defaults()
	repo, err := ParseRepository(build.Repository)
	if err != nil {
		return NewFieldError(field+".build.repository", "%s", err)
	}
	if err := checkBranch(build.Branch); err != nil {
		return NewFieldError(field+".build.branch", "%s", err)
	}
	if err := checkBuildPath(build.Path, true); err != nil {
		return NewFieldError(field+".build.path", "%s", err)
	}
	if err := checkBuildPath(build.Dockerfile, false); err != nil {
		return NewFieldError(field+".build.dockerfile", "%s", err)
	}

	switch build.Access {
	case BuildAccessPublic:
		if repo.SSH {
			return NewFieldError(field+".build.access", "an SSH repository can't be read anonymously: use %q", BuildAccessDeployKey)
		}
	case BuildAccessDeployKey:
		if !repo.SSH {
			return NewFieldError(field+".build.access", "%q needs an SSH repository (ssh://user@host/path or user@host:path), and this one is read over HTTPS", BuildAccessDeployKey)
		}
	case BuildAccessToken:
		if repo.SSH {
			return NewFieldError(field+".build.access", "%q needs an HTTPS repository, and this one is read over SSH", BuildAccessToken)
		}
	default:
		return NewFieldError(field+".build.access", "%q is not supported; it is %q, %q or %q", build.Access, BuildAccessPublic, BuildAccessDeployKey, BuildAccessToken)
	}

	if build.HostKey != "" {
		if !repo.SSH {
			return NewFieldError(field+".build.hostKey", "only an SSH repository has a host key")
		}
		if err := checkHostKey(build.HostKey); err != nil {
			return NewFieldError(field+".build.hostKey", "%s", err)
		}
	}
	return nil
}

// imageHasTagOrDigest reports whether an image reference names a tag or a
// digest. A port in the host ("localhost:5000/app") is not a tag: only the last
// path segment counts.
func imageHasTagOrDigest(image string) bool {
	if strings.Contains(image, "@") {
		return true
	}
	last := image[strings.LastIndex(image, "/")+1:]
	return strings.Contains(last, ":")
}

// checkBranch checks a branch name: plain characters, and none of the shapes Git
// refuses or that mean something else on a command line.
func checkBranch(s string) error {
	switch {
	case len(s) > maxBranchBytes || !branchRE.MatchString(s):
		return fmt.Errorf("%q must be 1 to %d letters, digits and . _ / -", s, maxBranchBytes)
	case strings.HasPrefix(s, "-") || strings.HasPrefix(s, "/"):
		return fmt.Errorf("%q must not start with %q", s, s[:1])
	case strings.Contains(s, "..") || strings.Contains(s, "//"):
		return fmt.Errorf("%q must not contain %q or %q", s, "..", "//")
	case strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".lock") || strings.HasSuffix(s, "."):
		return fmt.Errorf("%q must not end with %q, %q or %q", s, "/", ".", ".lock")
	}
	return nil
}

// checkBuildPath checks a relative folder (folder true: "." alone is the root)
// or file path inside the repository: segments of letters, digits and . _ -, no
// "..", nothing absolute.
func checkBuildPath(s string, folder bool) error {
	if s == "." && folder {
		return nil
	}
	if len(s) > maxBuildPathBytes {
		return fmt.Errorf("is %d bytes; at most %d are allowed", len(s), maxBuildPathBytes)
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("%q must be relative to the repository, not start with /", s)
	}
	for seg := range strings.SplitSeq(s, "/") {
		switch {
		case seg == "" || seg == "." || seg == "..":
			return fmt.Errorf("%q must be a plain relative path without empty, %q or %q segments", s, ".", "..")
		case !pathSegmentRE.MatchString(seg):
			return fmt.Errorf("%q must be letters, digits and . _ - in each segment", s)
		}
	}
	return nil
}

// checkHostKey checks a known_hosts key: "<type> <base64>" whose decoded body
// starts with the same type, as an SSH public key does.
func checkHostKey(s string) error {
	m := hostKeyRE.FindStringSubmatch(s)
	if len(s) > maxHostKeyBytes || m == nil {
		return fmt.Errorf("must be a host's public key as in known_hosts: \"<type> <base64>\", e.g. ssh-ed25519 AAAA…")
	}
	body, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil || len(body) < 4 {
		return fmt.Errorf("the key's base64 text is not valid")
	}
	n := binary.BigEndian.Uint32(body)
	if uint64(n) > uint64(len(body)-4) || string(body[4:4+n]) != m[1] {
		return fmt.Errorf("the key is not a %s key: its text names another type", m[1])
	}
	return nil
}
