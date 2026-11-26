package compose

import (
	"strings"
	"testing"
)

func TestReplaceImages(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		images map[string]string
		want   string
	}{
		{
			name:   "plain",
			text:   "services:\n  web:\n    image: nginx:1.25\n",
			images: map[string]string{"web": "nginx:1.26"},
			want:   "services:\n  web:\n    image: nginx:1.26\n",
		},
		{
			name:   "double quoted stays double quoted",
			text:   "services:\n  web:\n    image: \"nginx:1.25\"\n    ports: [\"80\"]\n",
			images: map[string]string{"web": "nginx:1.26"},
			want:   "services:\n  web:\n    image: \"nginx:1.26\"\n    ports: [\"80\"]\n",
		},
		{
			name:   "single quoted stays single quoted",
			text:   "services:\n  web:\n    image: 'nginx:1.25'\n",
			images: map[string]string{"web": "nginx:1.26"},
			want:   "services:\n  web:\n    image: 'nginx:1.26'\n",
		},
		{
			name:   "single quoted doubles a quote",
			text:   "services:\n  web:\n    image: 'a'\n",
			images: map[string]string{"web": "it's"},
			want:   "services:\n  web:\n    image: 'it''s'\n",
		},
		{
			name:   "flow mapping",
			text:   "services:\n  server: {image: ghcr.io/acme/server:1, expose: [\"3000\"]}\n",
			images: map[string]string{"server": "ghcr.io/acme/server:2"},
			want:   "services:\n  server: {image: ghcr.io/acme/server:2, expose: [\"3000\"]}\n",
		},
		{
			name:   "two services in one flow line",
			text:   "services: {web: {image: a:1}, db: {image: b:1}}\n",
			images: map[string]string{"web": "a:22", "db": "b:2"},
			want:   "services: {web: {image: a:22}, db: {image: b:2}}\n",
		},
		{
			name:   "trailing comment kept",
			text:   "services:\n  web:\n    image: traefik/whoami:latest   # pinned later\n",
			images: map[string]string{"web": "traefik/whoami:v1.10"},
			want:   "services:\n  web:\n    image: traefik/whoami:v1.10   # pinned later\n",
		},
		{
			name:   "two services at once, the third left alone",
			text:   "services:\n  web:\n    image: a:1\n  worker:\n    image: b:1\n  db:\n    image: c:1\n",
			images: map[string]string{"web": "a:2", "worker": "b:2"},
			want:   "services:\n  web:\n    image: a:2\n  worker:\n    image: b:2\n  db:\n    image: c:1\n",
		},
		{
			name:   "digest",
			text:   "services:\n  web:\n    image: repo/app:1\n",
			images: map[string]string{"web": "repo/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			want:   "services:\n  web:\n    image: repo/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n",
		},
		{
			name:   "everything else byte for byte",
			text:   "# the blog\r\n\r\nx-common: &common   # shared\r\n  restart: always\r\n\r\nservices:\r\n\r\n  web:\r\n    <<: *common\r\n    # the image\r\n    image:   \"nginx:1.25\"  # now\r\n    environment:\r\n      A: 'b'\r\n\r\n  db:\r\n    image: postgres:16\r\n# end\r\n",
			images: map[string]string{"web": "nginx:1.26"},
			want:   "# the blog\r\n\r\nx-common: &common   # shared\r\n  restart: always\r\n\r\nservices:\r\n\r\n  web:\r\n    <<: *common\r\n    # the image\r\n    image:   \"nginx:1.26\"  # now\r\n    environment:\r\n      A: 'b'\r\n\r\n  db:\r\n    image: postgres:16\r\n# end\r\n",
		},
		{
			name:   "same value is left as it is",
			text:   "services:\n  web:\n    image:   \"nginx:1.25\"\n",
			images: map[string]string{"web": "nginx:1.25"},
			want:   "services:\n  web:\n    image:   \"nginx:1.25\"\n",
		},
		{
			name:   "a value that reads as a number is quoted",
			text:   "services:\n  web:\n    image: a:1\n",
			images: map[string]string{"web": "1.5"},
			want:   "services:\n  web:\n    image: \"1.5\"\n",
		},
		{
			name:   "non-ASCII before the image on its line",
			text:   "services:\n  web: {name: \"café\", image: a:1}\n",
			images: map[string]string{"web": "a:2"},
			want:   "services:\n  web: {name: \"café\", image: a:2}\n",
		},
		{
			name:   "no images to change",
			text:   "services:\n  web:\n    image: a:1\n",
			images: map[string]string{},
			want:   "services:\n  web:\n    image: a:1\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReplaceImages(tt.text, tt.images)
			if err != nil {
				t.Fatalf("ReplaceImages() = %v", err)
			}
			if got != tt.want {
				t.Errorf("ReplaceImages() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestReplaceImagesRefuses(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		images map[string]string
		want   string // part of the error
	}{
		{
			name:   "a variable",
			text:   "services:\n  web:\n    image: ${IMAGE:-nginx:1}\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the image holds a variable",
		},
		{
			name:   "an alias image",
			text:   "x-img: &img nginx:1\nservices:\n  web:\n    image: *img\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the image is an alias",
		},
		{
			name:   "an anchored image",
			text:   "services:\n  web:\n    image: &img nginx:1\n  db:\n    image: *img\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the image has an anchor",
		},
		{
			name:   "a merge key gives the image",
			text:   "x-base: &base\n  image: nginx:1\nservices:\n  web:\n    <<: *base\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the service has no image key",
		},
		{
			name:   "the service is an alias",
			text:   "x-svc: &svc\n  image: nginx:1\nservices:\n  web: *svc\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the service is not a plain mapping",
		},
		{
			name:   "a missing service",
			text:   "services:\n  web:\n    image: a:1\n",
			images: map[string]string{"api": "a:2"},
			want:   "services.api.image: the service is not in the file",
		},
		{
			name:   "a service with no image",
			text:   "services:\n  web:\n    build: .\n",
			images: map[string]string{"web": "a:2"},
			want:   "services.web.image: the service has no image key",
		},
		{
			name:   "a literal block scalar",
			text:   "services:\n  web:\n    image: |\n      nginx:1\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image: the image is a multi-line block scalar",
		},
		{
			name:   "a multi-line plain scalar",
			text:   "services:\n  web:\n    image: nginx\n      :1\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image:",
		},
		{
			name:   "a tagged image",
			text:   "services:\n  web:\n    image: !!str nginx:1\n",
			images: map[string]string{"web": "nginx:2"},
			want:   "services.web.image:",
		},
		{
			name:   "two documents",
			text:   "services:\n  web:\n    image: a:1\n---\nservices: {}\n",
			images: map[string]string{"web": "a:2"},
			want:   "more than one YAML document",
		},
		{
			name:   "not a mapping",
			text:   "- a\n- b\n",
			images: map[string]string{"web": "a:2"},
			want:   "not a mapping",
		},
		{
			name:   "no services",
			text:   "name: x\n",
			images: map[string]string{"web": "a:2"},
			want:   "services: not found",
		},
		{
			name:   "a new image with a variable",
			text:   "services:\n  web:\n    image: a:1\n",
			images: map[string]string{"web": "a:${TAG}"},
			want:   "services.web.image:",
		},
		{
			name:   "an empty new image",
			text:   "services:\n  web:\n    image: a:1\n",
			images: map[string]string{"web": ""},
			want:   "services.web.image:",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReplaceImages(tt.text, tt.images)
			if err == nil {
				t.Fatalf("ReplaceImages() = %q, want an error", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}
