package app

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// hostKeyOf makes a known_hosts key of typ whose body names typ (and nothing
// else), which is all checkHostKey reads.
func hostKeyOf(typ, bodyType string) string {
	body := binary.BigEndian.AppendUint32(nil, uint32(len(bodyType)))
	body = append(body, bodyType...)
	body = append(body, "key bytes"...)
	return typ + " " + base64.StdEncoding.EncodeToString(body)
}

// builtApp is an app whose service web is built from a public repository.
func builtApp(mutate func(*Service)) App {
	a := validApp()
	a.Services[0].Image = "ghcr.io/acme/shop"
	a.Services[0].Build = &Build{Repository: "https://github.com/acme/shop.git"}
	if mutate != nil {
		mutate(&a.Services[0])
	}
	return a
}

func TestValidateBuild(t *testing.T) {
	ed := hostKeyOf("ssh-ed25519", "ssh-ed25519")

	tests := []struct {
		name      string
		mutate    func(*Service)
		wantField string // empty means valid
	}{
		{"public https repository", nil, ""},
		{"every field written", func(s *Service) {
			s.Build = &Build{Repository: "https://git.example.com/acme/shop", Branch: "release/1.x", Path: "services/web", Dockerfile: "docker/Dockerfile.prod", Access: BuildAccessToken}
		}, ""},
		{"deploy key over ssh url", func(s *Service) {
			s.Build = &Build{Repository: "ssh://git@git.example.com:2222/acme/shop.git", Access: BuildAccessDeployKey, HostKey: ed}
		}, ""},
		{"deploy key over scp address", func(s *Service) {
			s.Build = &Build{Repository: "git@github.com:acme/shop.git", Access: BuildAccessDeployKey}
		}, ""},
		{"http on loopback", func(s *Service) {
			s.Build = &Build{Repository: "http://127.0.0.1:8098/app.git"}
			s.Image = "127.0.0.1:5001/gate/app"
		}, ""},
		{"image on a registry with a port", func(s *Service) { s.Image = "registry.example.com:5000/acme/shop" }, ""},

		{"image with a tag", func(s *Service) { s.Image = "ghcr.io/acme/shop:main" }, "services[0].image"},
		{"image with a digest", func(s *Service) { s.Image = "ghcr.io/acme/shop@sha256:" + strings.Repeat("a", 64) }, "services[0].image"},
		{"image with a port and a tag", func(s *Service) { s.Image = "localhost:5000/shop:1" }, "services[0].image"},

		{"repository missing", func(s *Service) { s.Build.Repository = "" }, "services[0].build.repository"},
		{"repository plain http", func(s *Service) { s.Build.Repository = "http://git.example.com/acme/shop.git" }, "services[0].build.repository"},
		{"repository with credentials", func(s *Service) { s.Build.Repository = "https://ana:secret@github.com/acme/shop.git" }, "services[0].build.repository"},
		{"repository with a user only", func(s *Service) { s.Build.Repository = "https://ana@github.com/acme/shop.git" }, "services[0].build.repository"},
		{"repository with a query", func(s *Service) { s.Build.Repository = "https://github.com/acme/shop.git?token=x" }, "services[0].build.repository"},
		{"repository with a fragment", func(s *Service) { s.Build.Repository = "https://github.com/acme/shop.git#main" }, "services[0].build.repository"},
		{"repository with a space", func(s *Service) { s.Build.Repository = "https://github.com/acme/my shop.git" }, "services[0].build.repository"},
		{"repository with a shell character", func(s *Service) { s.Build.Repository = "https://github.com/acme/$(x).git" }, "services[0].build.repository"},
		{"repository with the file scheme", func(s *Service) { s.Build.Repository = "file:///srv/git/shop.git" }, "services[0].build.repository"},
		{"repository with no host", func(s *Service) { s.Build.Repository = "https:///acme/shop.git" }, "services[0].build.repository"},
		{"repository with no path", func(s *Service) { s.Build.Repository = "https://github.com" }, "services[0].build.repository"},
		{"repository not an address", func(s *Service) { s.Build.Repository = "acme/shop" }, "services[0].build.repository"},
		{"repository too long", func(s *Service) { s.Build.Repository = "https://github.com/" + strings.Repeat("a", 600) }, "services[0].build.repository"},
		{"ssh url with no user", func(s *Service) {
			s.Build = &Build{Repository: "ssh://github.com/acme/shop.git", Access: BuildAccessDeployKey}
		}, "services[0].build.repository"},
		{"ssh url with a password", func(s *Service) {
			s.Build = &Build{Repository: "ssh://git:pw@github.com/acme/shop.git", Access: BuildAccessDeployKey}
		}, "services[0].build.repository"},
		{"ssh url with a bad port", func(s *Service) {
			s.Build = &Build{Repository: "ssh://git@github.com:70000/acme/shop.git", Access: BuildAccessDeployKey}
		}, "services[0].build.repository"},

		{"branch with ..", func(s *Service) { s.Build.Branch = "a/../b" }, "services[0].build.branch"},
		{"branch starting with -", func(s *Service) { s.Build.Branch = "-x" }, "services[0].build.branch"},
		{"branch starting with /", func(s *Service) { s.Build.Branch = "/x" }, "services[0].build.branch"},
		{"branch with //", func(s *Service) { s.Build.Branch = "a//b" }, "services[0].build.branch"},
		{"branch ending with /", func(s *Service) { s.Build.Branch = "a/" }, "services[0].build.branch"},
		{"branch ending with .lock", func(s *Service) { s.Build.Branch = "a.lock" }, "services[0].build.branch"},
		{"branch with a space", func(s *Service) { s.Build.Branch = "a b" }, "services[0].build.branch"},
		{"branch too long", func(s *Service) { s.Build.Branch = strings.Repeat("a", 256) }, "services[0].build.branch"},

		{"path absolute", func(s *Service) { s.Build.Path = "/srv" }, "services[0].build.path"},
		{"path up", func(s *Service) { s.Build.Path = "../x" }, "services[0].build.path"},
		{"path with a dot segment", func(s *Service) { s.Build.Path = "a/./b" }, "services[0].build.path"},
		{"path with a trailing slash", func(s *Service) { s.Build.Path = "a/" }, "services[0].build.path"},
		{"path with a bad character", func(s *Service) { s.Build.Path = "a b" }, "services[0].build.path"},
		{"dockerfile absolute", func(s *Service) { s.Build.Dockerfile = "/Dockerfile" }, "services[0].build.dockerfile"},
		{"dockerfile up", func(s *Service) { s.Build.Dockerfile = "a/../../Dockerfile" }, "services[0].build.dockerfile"},
		{"dockerfile is the folder", func(s *Service) { s.Build.Dockerfile = "." }, "services[0].build.dockerfile"},

		{"access unknown", func(s *Service) { s.Build.Access = "password" }, "services[0].build.access"},
		{"deploy key over https", func(s *Service) { s.Build.Access = BuildAccessDeployKey }, "services[0].build.access"},
		{"token over ssh", func(s *Service) {
			s.Build = &Build{Repository: "git@github.com:acme/shop.git", Access: BuildAccessToken}
		}, "services[0].build.access"},
		{"public over ssh", func(s *Service) { s.Build = &Build{Repository: "git@github.com:acme/shop.git"} }, "services[0].build.access"},

		{"host key on https", func(s *Service) { s.Build.HostKey = ed }, "services[0].build.hostKey"},
		{"host key not a key", func(s *Service) {
			s.Build = &Build{Repository: "git@git.example.com:acme/shop.git", Access: BuildAccessDeployKey, HostKey: "not a key"}
		}, "services[0].build.hostKey"},
		{"host key of another type than it says", func(s *Service) {
			s.Build = &Build{Repository: "git@git.example.com:acme/shop.git", Access: BuildAccessDeployKey, HostKey: hostKeyOf("ssh-ed25519", "ssh-rsa")}
		}, "services[0].build.hostKey"},
		{"host key with bad base64", func(s *Service) {
			s.Build = &Build{Repository: "git@git.example.com:acme/shop.git", Access: BuildAccessDeployKey, HostKey: "ssh-ed25519 AAAA=AAA"}
		}, "services[0].build.hostKey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := builtApp(tc.mutate)
			err := ValidateApp(a)
			if tc.wantField == "" {
				if err != nil {
					t.Fatalf("ValidateApp() = %v, want nil", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.wantField {
				t.Fatalf("ValidateApp() = %v, want a *FieldError for %q", err, tc.wantField)
			}
			if fe.Message == "" {
				t.Errorf("Message is empty for %q", fe.Field)
			}
		})
	}
}

// An app has one built service, and the second is the one refused.
func TestValidateBuildOnePerApp(t *testing.T) {
	a := builtApp(nil)
	a.Services[1].Image = "ghcr.io/acme/worker"
	a.Services[1].Build = &Build{Repository: "https://github.com/acme/worker.git"}
	var fe *FieldError
	if err := ValidateApp(a); !errors.As(err, &fe) || fe.Field != "services[1].build" {
		t.Fatalf("ValidateApp() = %v, want a *FieldError for services[1].build", err)
	}
}

// A service's own problem is reported before the second built service.
func TestValidateBuildIndexInErrors(t *testing.T) {
	a := builtApp(nil)
	a.Services[1].Image = "ghcr.io/acme/worker:1"
	a.Services[1].Build = &Build{Repository: "https://github.com/acme/worker.git"}
	var fe *FieldError
	if err := ValidateApp(a); !errors.As(err, &fe) || fe.Field != "services[1].image" {
		t.Fatalf("ValidateApp() = %v, want the image of services[1] first", err)
	}
}

func TestBuildDefaults(t *testing.T) {
	got := Build{Repository: "https://github.com/acme/shop.git"}.Defaults()
	want := Build{Repository: "https://github.com/acme/shop.git", Branch: "main", Path: ".", Dockerfile: "Dockerfile", Access: "public"}
	if got != want {
		t.Errorf("Defaults() = %+v, want %+v", got, want)
	}
	set := Build{Repository: "r", Branch: "dev", Path: "web", Dockerfile: "D", Access: BuildAccessToken, HostKey: "k"}
	if set.Defaults() != set {
		t.Errorf("Defaults() changed a build that sets every field: %+v", set.Defaults())
	}
}

// No stored app's JSON changes: a service without a build writes no "build".
func TestServiceWithoutBuildJSONUnchanged(t *testing.T) {
	b, err := json.Marshal(validApp().Services[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "build") {
		t.Errorf("a service without a build wrote it: %s", b)
	}
	out, err := json.Marshal(builtApp(nil).Services[0].Build)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"repository":"https://github.com/acme/shop.git"}` {
		t.Errorf("build JSON = %s", out)
	}
}

func TestBuiltService(t *testing.T) {
	if _, ok := validApp().BuiltService(); ok {
		t.Error("an app with no build has no built service")
	}
	a := validApp()
	a.Services[1].Build = &Build{}
	if i, ok := a.BuiltService(); !ok || i != 1 {
		t.Errorf("BuiltService() = %d, %v, want 1, true", i, ok)
	}
}

func TestParseRepository(t *testing.T) {
	tests := []struct {
		in   string
		want Repository
	}{
		{"https://github.com/acme/shop.git", Repository{Host: "github.com", Path: "acme/shop.git"}},
		{"https://GitHub.com:8443/acme/shop", Repository{Host: "github.com", Port: 8443, Path: "acme/shop"}},
		{"http://localhost:8098/app.git", Repository{Host: "localhost", Port: 8098, Path: "app.git"}},
		{"http://[::1]:8098/app.git", Repository{Host: "::1", Port: 8098, Path: "app.git"}},
		{"ssh://git@git.example.com:2222/acme/shop.git", Repository{SSH: true, Host: "git.example.com", Port: 2222, Path: "acme/shop.git"}},
		{"ssh://git@github.com/acme/shop.git", Repository{SSH: true, Host: "github.com", Path: "acme/shop.git"}},
		{"git@github.com:acme/shop.git", Repository{SSH: true, Host: "github.com", Path: "acme/shop.git"}},
		{"git@GitLab.com:/acme/shop.git", Repository{SSH: true, Host: "gitlab.com", Path: "/acme/shop.git"}},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseRepository(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("ParseRepository(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRepositoryLoopback(t *testing.T) {
	for in, want := range map[string]bool{
		"http://127.0.0.1:8098/app.git":     true,
		"http://localhost/app.git":          true,
		"http://[::1]:8098/app.git":         true,
		"ssh://git@127.0.0.1:2222/app.git":  true,
		"https://github.com/acme/shop.git":  false,
		"git@example.com:acme/shop.git":     false,
		"https://127.0.0.1.example.com/a/b": false,
	} {
		r, err := ParseRepository(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if r.Loopback() != want {
			t.Errorf("Loopback(%s) = %v, want %v", in, r.Loopback(), want)
		}
	}
}

func TestHasBuiltinHostKeys(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
		want bool
	}{
		{"github.com", 0, true}, {"GitHub.com", 22, true}, {"gitlab.com", 0, true},
		{"github.com", 2222, false}, {"git.example.com", 0, false}, {"ssh.github.com", 0, false},
	} {
		if got := HasBuiltinHostKeys(tc.host, tc.port); got != tc.want {
			t.Errorf("HasBuiltinHostKeys(%q, %d) = %v, want %v", tc.host, tc.port, got, tc.want)
		}
	}
}
