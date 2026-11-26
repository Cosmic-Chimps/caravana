package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/app"
)

const configsHead = "services:\n  web:\n    image: opensearchproject/opensearch:2.19.1\n"

func translateConfigs(t *testing.T, serviceYAML, topYAML string, services map[string]Overlay) (app.App, []app.Warning, error) {
	t.Helper()
	return Translate("shop", Request{Compose: configsHead + serviceYAML + topYAML, Services: services})
}

// Inline configs become content files: the short form at /<name>, the long form with its target, mode and owner.
func TestConfigsBecomeFiles(t *testing.T) {
	top := "configs:\n  opensearch:\n    content: |\n      cluster.name: shop\n      note: ${HOME:-x} $${KEPT} {{ not a template }}\n  motd:\n    name: ignored_external_name\n    content: hello\n"
	svc := "    configs:\n      - motd\n      - source: opensearch\n        target: /usr/share/opensearch/config/opensearch.yml\n        mode: 0440\n        uid: \"1000\"\n        gid: \"0\"\n"
	a, _, err := translateConfigs(t, svc, top, nil)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	want := []app.File{
		{Path: "/motd", Content: "hello"},
		{Path: "/usr/share/opensearch/config/opensearch.yml", Content: "cluster.name: shop\nnote: x ${KEPT} {{ not a template }}\n", Mode: "0440", UID: new(1000), GID: new(0)},
	}
	if got := a.Services[0].Files; !reflect.DeepEqual(got, want) {
		t.Errorf("Files = %+v\nwant %+v", got, want)
	}

	// A file that no service uses makes nothing; a service without configs has no files.
	a, _, err = translateConfigs(t, "", top, nil)
	if err != nil || a.Services[0].Files != nil {
		t.Errorf("unused configs: %+v, %v", a.Services[0].Files, err)
	}
}

func TestConfigRefusals(t *testing.T) {
	tests := []struct {
		name, svc, top, field, says string
	}{
		{"a file", "", "configs:\n  c:\n    file: ./app.yml\n", "configs.c.file", "paste the file's content inline"},
		{"an environment variable", "", "configs:\n  c:\n    environment: APP_CONF\n", "configs.c.environment", "paste the file's content inline"},
		{"an external config", "", "configs:\n  c:\n    external: true\n", "configs.c.external", "paste the file's content inline"},
		{"no content", "", "configs:\n  c:\n    name: x\n", "compose", "one of file|environment|content must be set"},
		{"content with a NUL", "", "configs:\n  c:\n    content: \"a\\0b\"\n", "configs.c.content", "NUL"},
		{"content too large", "", "configs:\n  c:\n    content: " + strings.Repeat("x", 70000) + "\n", "configs.c.content", "at most"},
		{"a source that isn't declared", "    configs:\n      - nope\n", "", "compose", ""},
		{"a relative target", "    configs:\n      - source: c\n        target: etc/app.yml\n", "configs:\n  c:\n    content: x\n", "services.web.configs[0].target", "absolute"},
		{"a target in /proc", "    configs:\n      - source: c\n        target: /proc/x\n", "configs:\n  c:\n    content: x\n", "services.web.configs[0].target", "/proc"},
		{"a mode above 0777", "    configs:\n      - source: c\n        target: /a\n        mode: 01777\n", "configs:\n  c:\n    content: x\n", "services.web.configs[0].mode", "0777"},
		{"an owner that is a name", "    configs:\n      - source: c\n        target: /a\n        uid: opensearch\n", "configs:\n  c:\n    content: x\n", "services.web.configs[0].uid", "number"},
		{"an owner out of range", "    configs:\n      - source: c\n        target: /a\n        gid: \"70000\"\n", "configs:\n  c:\n    content: x\n", "services.web.configs[0].gid", "65535"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := translateConfigs(t, tt.svc, tt.top, nil)
			fe, ok := err.(*app.FieldError)
			if !ok || fe.Field != tt.field || !strings.Contains(fe.Message, tt.says) {
				t.Errorf("Translate = %v, want a field error on %s saying %q", err, tt.field, tt.says)
			}
		})
	}
}

// The overlay's files are added after the configs' files, content or secret; a path in both is refused at the
// overlay's entry, and what the model refuses is named by the file's own paths.
func TestOverlayFiles(t *testing.T) {
	svc := "    configs:\n      - source: c\n        target: /etc/a.yml\n"
	top := "configs:\n  c:\n    content: x\n"
	ov := map[string]Overlay{"web": {
		Secrets: []app.Secret{{Name: "TLS_KEY", Value: new("pem")}},
		Files: []app.File{
			{Path: "/certs/node.key", Secret: "TLS_KEY", Mode: "0400"},
			{Path: "/etc/b.yml", Content: "b"},
		},
	}}
	a, _, err := translateConfigs(t, svc, top, ov)
	if err != nil {
		t.Fatalf("Translate: %v", err)
	}
	var paths []string
	for _, f := range a.Services[0].Files {
		paths = append(paths, f.Path)
	}
	if want := []string{"/etc/a.yml", "/certs/node.key", "/etc/b.yml"}; !reflect.DeepEqual(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}

	same := map[string]Overlay{"web": {Files: []app.File{{Path: "/etc/b.yml", Content: "b"}, {Path: "/etc/a.yml", Content: "other"}}}}
	_, _, err = translateConfigs(t, svc, top, same)
	if fe, ok := err.(*app.FieldError); !ok || fe.Field != "services.web.files[1].path" {
		t.Errorf("a path in the configs and the overlay: %v", err)
	}

	// The overlay's secret file must name one of its secrets.
	noSecret := map[string]Overlay{"web": {Files: []app.File{{Path: "/certs/k", Secret: "TLS_KEY"}}}}
	_, _, err = translateConfigs(t, "", "", noSecret)
	if fe, ok := err.(*app.FieldError); !ok || fe.Field != "services.web.files[0].secret" {
		t.Errorf("a secret file without the secret: %v", err)
	}
	bad := map[string]Overlay{"web": {Files: []app.File{{Path: "/etc/x", Content: "x", Mode: "999"}}}}
	_, _, err = translateConfigs(t, "", "", bad)
	if fe, ok := err.(*app.FieldError); !ok || fe.Field != "services.web.files[0].mode" {
		t.Errorf("a bad mode: %v", err)
	}
	if (Overlay{Files: []app.File{{Path: "/a", Content: "x"}}}).Empty() {
		t.Error("an overlay with files is empty")
	}
}
