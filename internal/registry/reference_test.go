package registry

import (
	"strings"
	"testing"
)

const testDigest = "sha256:c4717a8d1f0134a7444e24f881160e033991f23027c6c5a9a3f8fd22e70d1d44"

func TestParse(t *testing.T) {
	tests := []struct {
		image string
		want  Reference
	}{
		{"traefik/whoami:latest", Reference{Name: "traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Tag: "latest"}},
		{"traefik/whoami", Reference{Name: "traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Tag: "latest"}},
		{"nginx", Reference{Name: "nginx", Registry: "docker.io", Repository: "library/nginx", Tag: "latest"}},
		{"docker.io/library/nginx:1.27", Reference{Name: "docker.io/library/nginx", Registry: "docker.io", Repository: "library/nginx", Tag: "1.27"}},
		{"index.docker.io/traefik/whoami:v1", Reference{Name: "index.docker.io/traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Tag: "v1"}},
		{"ghcr.io/acme/shop:main", Reference{Name: "ghcr.io/acme/shop", Registry: "ghcr.io", Repository: "acme/shop", Tag: "main"}},
		{"ghcr.io/acme/team/api_v2:1.0.0-rc.1", Reference{Name: "ghcr.io/acme/team/api_v2", Registry: "ghcr.io", Repository: "acme/team/api_v2", Tag: "1.0.0-rc.1"}},
		{"quay.io/x/y:1", Reference{Name: "quay.io/x/y", Registry: "quay.io", Repository: "x/y", Tag: "1"}},
		{"registry.example.com/x", Reference{Name: "registry.example.com/x", Registry: "registry.example.com", Repository: "x", Tag: "latest"}},
		{"registry.example.com:5000/acme/shop:v1", Reference{Name: "registry.example.com:5000/acme/shop", Registry: "registry.example.com:5000", Repository: "acme/shop", Tag: "v1"}},
		{"localhost:5000/x:1", Reference{Name: "localhost:5000/x", Registry: "localhost:5000", Repository: "x", Tag: "1"}},
		{"localhost/x", Reference{Name: "localhost/x", Registry: "localhost", Repository: "x", Tag: "latest"}},
		{"127.0.0.1:5000/gate/busybox:v1", Reference{Name: "127.0.0.1:5000/gate/busybox", Registry: "127.0.0.1:5000", Repository: "gate/busybox", Tag: "v1"}},
		{"[::1]:5000/x", Reference{Name: "[::1]:5000/x", Registry: "[::1]:5000", Repository: "x", Tag: "latest"}},
		{"Registry.Example.COM:5000/acme/shop:v1", Reference{Name: "registry.example.com:5000/acme/shop", Registry: "registry.example.com:5000", Repository: "acme/shop", Tag: "v1"}},
		{"Index.Docker.IO/traefik/whoami", Reference{Name: "index.docker.io/traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Tag: "latest"}},
		{"registry.example.com:5000/acme/shop@" + testDigest, Reference{Name: "registry.example.com:5000/acme/shop", Registry: "registry.example.com:5000", Repository: "acme/shop", Digest: testDigest}},
		{"traefik/whoami@" + testDigest, Reference{Name: "traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Digest: testDigest}},
		{"traefik/whoami:v1@" + testDigest, Reference{Name: "traefik/whoami", Registry: "docker.io", Repository: "traefik/whoami", Digest: testDigest, IgnoredTag: "v1"}},
	}
	for _, tc := range tests {
		t.Run(tc.image, func(t *testing.T) {
			got, err := Parse(tc.image)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("Parse() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		image   string
		wantMsg string
	}{
		{"", "is required"},
		{"Traefik/whoami", "not a valid image name"},
		{"traefik//whoami", "not a valid image name"},
		{"whoami:bad tag!", "not a valid tag"},
		{"whoami:", "not a valid tag"},
		{"x@sha256:short", "sha256:<64 lowercase hex>"},
		{"x@md5:" + strings.Repeat("a", 32), "sha256:<64 lowercase hex>"},
		{"registry.example.com/", "not a valid image name"},
		{"registry.example.com/Acme/shop", "not a valid image name"},
		{"bad_host.example.com/x", "not a valid registry host"},
		{"registry.example.com:99999999/x", "not a valid registry host"},
		{"-registry.example.com/x", "not a valid registry host"},
		{"registry..example.com/x", "not a valid registry host"},
		{"[zz]:5000/x", "not a valid registry host"},
	}
	for _, tc := range tests {
		t.Run(tc.image, func(t *testing.T) {
			_, err := Parse(tc.image)
			if err == nil || !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("Parse(%q) error = %v, want one containing %q", tc.image, err, tc.wantMsg)
			}
		})
	}
}

func TestEndpoint(t *testing.T) {
	tests := map[string]string{
		"docker.io":                "https://registry-1.docker.io",
		"ghcr.io":                  "https://ghcr.io",
		"registry.example.com":     "https://registry.example.com",
		"registry.example.com:443": "https://registry.example.com:443",
		"localhost":                "http://localhost",
		"localhost:5000":           "http://localhost:5000",
		"127.0.0.1":                "http://127.0.0.1",
		"127.0.0.1:5000":           "http://127.0.0.1:5000",
		"127.5.6.7:5000":           "http://127.5.6.7:5000",
		"[::1]":                    "http://[::1]",
		"[::1]:5000":               "http://[::1]:5000",
		// Looking like loopback isn't being loopback.
		"localhost.example.com:5000": "https://localhost.example.com:5000",
		"127.0.0.1.nip.io":           "https://127.0.0.1.nip.io",
		"10.0.0.5:5000":              "https://10.0.0.5:5000",
	}
	for host, want := range tests {
		t.Run(host, func(t *testing.T) {
			if got := Endpoint(host); got != want {
				t.Errorf("Endpoint(%q) = %q, want %q", host, got, want)
			}
		})
	}
}

func TestPinned(t *testing.T) {
	for image, want := range map[string]string{
		"traefik/whoami:latest":  "traefik/whoami@" + testDigest,
		"nginx":                  "nginx@" + testDigest,
		"ghcr.io/acme/shop:main": "ghcr.io/acme/shop@" + testDigest,
		"registry.example.com:5000/acme/shop:main": "registry.example.com:5000/acme/shop@" + testDigest,
	} {
		ref, err := Parse(image)
		if err != nil {
			t.Fatalf("Parse(%q): %v", image, err)
		}
		if got := ref.Pinned(testDigest); got != want {
			t.Errorf("Pinned(%q) = %q, want %q", image, got, want)
		}
	}
}
