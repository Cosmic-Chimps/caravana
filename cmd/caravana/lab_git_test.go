package main

// What the build lab steps share (feature 050 phase E): a throwaway Git server and a registry with
// htpasswd, both Nomad jobs on the lab VM's loopback (Lima forwards their ports to the Mac, so the
// test pushes commits from the Mac and the build job reads them in the VM), and the helpers that
// make commits and the app's body. The gate step `build-from-git` and the console step
// `console-build` each make their own buildLab on their own ports, so neither waits on the other.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/nomad/api"
	"golang.org/x/crypto/ssh"

	"github.com/cosmic-chimps/caravana/internal/deploy"
)

// gitServerHCL is the Git server's job: sshd on 127.0.0.1:@SSH@ as the user git (git-shell, a key
// from the job's own Nomad Variable) and a smart-HTTP server (busybox httpd running git-http-backend as a CGI,
// which a shallow clone needs: the dumb protocol refuses --depth) on 127.0.0.1:@HTTP@ over the same bare
// repository /srv/git/app.git, at /cgi-bin/git/app.git.
// It was tried in the lab before it was written here (alpine pinned by digest like the other jobs).
const gitServerHCL = `
job "@JOB@" {
  type = "service"
  group "g" {
    network { mode = "host" }
    task "git" {
      driver = "docker"
      config {
        image = "alpine@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f"
        network_mode = "host"
        entrypoint = ["sh", "-c"]
        args = ["set -e; apk add --no-cache git git-daemon openssh-server busybox-extras >/dev/null; git config --system safe.directory '*'; mkdir -p /srv/www/cgi-bin; printf '#!/bin/sh\\nexport GIT_PROJECT_ROOT=/srv/git GIT_HTTP_EXPORT_ALL=1\\nexec /usr/libexec/git-core/git-http-backend\\n' > /srv/www/cgi-bin/git; chmod +x /srv/www/cgi-bin/git; adduser -D -s /usr/bin/git-shell git; passwd -u git >/dev/null 2>&1 || true; sed -i s/^git:!/git:*/ /etc/shadow; mkdir -p /srv/git /home/git/.ssh; git init -q --bare -b main /srv/git/app.git; cp /srv/git/app.git/hooks/post-update.sample /srv/git/app.git/hooks/post-update; chmod +x /srv/git/app.git/hooks/post-update; git -C /srv/git/app.git update-server-info; chown -R git:git /srv/git /home/git; ssh-keygen -A >/dev/null; (httpd -f -p 127.0.0.1:@HTTP@ -h /srv/www &) ; exec /usr/sbin/sshd -D -e -p @SSH@ -o ListenAddress=127.0.0.1 -o PasswordAuthentication=no -o AuthorizedKeysFile=/local/authorized_keys -o StrictModes=no"]
      }
      template {
        destination = "local/authorized_keys"
        change_mode = "noop"
        data = "{{ with nomadVar \"nomad/jobs/@JOB@\" }}{{ .keys }}{{ end }}\n"
      }
      resources { memory = 64 }
    }
  }
}
`

// buildLab is one registry and one Git server in the lab VM, and a working copy on the Mac.
type buildLab struct {
	g                       *gate
	gitJob, regJob          string
	sshPort, httpPort       int
	regPort                 int
	regUser, regPass        string
	dir                     string // the temp directory: the key, the working copy
	keyPath                 string // the test's own private key (OpenSSH PEM)
	pubLine                 string // its authorized_keys line
	work                    string // the working copy
	branchSHA               map[string]string
	started, registryListed bool
}

// newBuildLab makes the key and the directory; start brings the jobs up. The names and ports are
// the caller's, so the two steps never share a job or a port.
func newBuildLab(t *testing.T, g *gate, gitJob, regJob string, sshPort, httpPort, regPort int) *buildLab {
	t.Helper()
	l := &buildLab{
		g: g, gitJob: gitJob, regJob: regJob, sshPort: sshPort, httpPort: httpPort, regPort: regPort,
		regUser: "b" + g.run, regPass: "pw-" + g.run, dir: t.TempDir(), branchSHA: map[string]string{},
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "caravana lab test")
	if err != nil {
		t.Fatal(err)
	}
	l.keyPath = filepath.Join(l.dir, "id_ed25519")
	if err := os.WriteFile(l.keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	l.pubLine = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " lab-test"
	return l
}

func (l *buildLab) regHost() string { return "127.0.0.1:" + strconv.Itoa(l.regPort) }

// httpURL is the public (smart HTTP) address of the repository, as the build job reads it.
func (l *buildLab) httpURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/cgi-bin/git/app.git", l.httpPort)
}

// sshURL is the repository over SSH, as the build job and the test's pushes read it.
func (l *buildLab) sshURL() string {
	return fmt.Sprintf("ssh://git@127.0.0.1:%d/srv/git/app.git", l.sshPort)
}

// start registers the registry and the Git server, saves the registry's credentials in Caravana
// and waits for all three to answer. stop (also run at the test's end) removes everything.
func (l *buildLab) start(t *testing.T) {
	t.Helper()
	l.stop() // leftovers of an aborted run
	t.Cleanup(l.stop)
	nomad := l.g.target.Nomad

	if _, _, err := nomad.Jobs().Register(gateRegistryJobOn(l.regJob, l.regHost(), l.regUser, l.regPass, l.regUser+"b", l.regPass+"b"), nil); err != nil {
		t.Fatalf("register the registry job: %v", err)
	}
	if _, err := nomad.Variables().Delete("nomad/jobs/"+l.gitJob, nil); err != nil {
		_ = err // none yet is fine
	}
	if _, _, err := nomad.Variables().Create(&api.Variable{
		Path: "nomad/jobs/" + l.gitJob, Items: api.VariableItems{"keys": l.pubLine + "\n"},
	}, nil); err != nil {
		t.Fatalf("write the Git server's keys: %v", err)
	}
	job, err := nomad.Jobs().ParseHCLOpts(&api.JobsParseRequest{
		JobHCL:       strings.NewReplacer("@JOB@", l.gitJob, "@SSH@", strconv.Itoa(l.sshPort), "@HTTP@", strconv.Itoa(l.httpPort)).Replace(gitServerHCL),
		Canonicalize: true,
	})
	if err != nil {
		t.Fatalf("parse the Git server's job: %v", err)
	}
	if _, _, err := nomad.Jobs().Register(job, nil); err != nil {
		t.Fatalf("register the Git server job: %v", err)
	}
	l.started = true

	waitFor(t, "the registry to answer 401 on "+l.regHost(), 3*time.Minute, func() bool {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://" + l.regHost() + "/v2/")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized && resp.Header.Get("Docker-Distribution-Api-Version") != ""
	})
	waitFor(t, "sshd of the Git server on "+strconv.Itoa(l.sshPort), 5*time.Minute, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", l.sshPort), 3*time.Second)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		banner := make([]byte, 4)
		_, err = io.ReadFull(conn, banner)
		return err == nil && string(banner) == "SSH-"
	})
	waitFor(t, "the Git server's HTTP side", time.Minute, func() bool {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/cgi-bin/git/app.git/info/refs?service=git-upload-pack", l.httpPort))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})

	// The registry's credentials, for the pushes and the pulls of the apps' images.
	payload, _ := json.Marshal(map[string]string{"username": l.regUser, "password": l.regPass})
	resp := l.g.api(t, http.MethodPut, "/v1/registries/"+l.regHost(), "application/json", string(payload))
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("putRegistry %s = %d: %s", l.regHost(), resp.StatusCode, raw)
	}
	l.registryListed = true

	l.work = filepath.Join(l.dir, "work")
	if err := os.MkdirAll(l.work, 0o755); err != nil {
		t.Fatal(err)
	}
	l.git(t, "init", "-q", "-b", "main")
	l.git(t, "remote", "add", "origin", l.sshURL())
}

// stop removes the jobs, the variables and the registry entry; every step of it may fail quietly.
func (l *buildLab) stop() {
	nomad := l.g.target.Nomad
	for _, id := range []string{l.gitJob, l.regJob} {
		_, _, _ = nomad.Jobs().Deregister(id, true, nil)
	}
	_, _ = nomad.Variables().Delete("nomad/jobs/"+l.gitJob, nil)
	if resp, err := l.g.apiRequest(http.MethodDelete, "/v1/registries/"+l.regHost(), "", ""); err == nil {
		resp.Body.Close()
	}
	_, _ = nomad.Variables().Delete(deploy.RegistryVariablePath(l.regHost()), nil)
}

// setKeys is the Git server's authorized_keys: the test's own key and any others given.
func (l *buildLab) setKeys(t *testing.T, others ...string) {
	t.Helper()
	lines := append([]string{l.pubLine}, others...)
	if _, _, err := l.g.target.Nomad.Variables().Create(&api.Variable{
		Path: "nomad/jobs/" + l.gitJob, Items: api.VariableItems{"keys": strings.Join(lines, "\n") + "\n"},
	}, nil); err != nil {
		t.Fatalf("write the Git server's keys: %v", err)
	}
	// Nomad renders the template again within a few seconds; a push with the new key is the proof.
}

// waitKeyAccepted waits for the Git server to accept the private key (an OpenSSH PEM): Nomad renders the
// authorized_keys template again a few seconds after the variable changes, and the build job's clone
// doesn't retry. `git ls-remote` succeeds only when the login does.
func (l *buildLab) waitKeyAccepted(t *testing.T, privatePEM string) {
	t.Helper()
	path := filepath.Join(l.dir, "deploy_key")
	if err := os.WriteFile(path, []byte(privatePEM), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the Git server to accept the new deploy key", time.Minute, func() bool {
		cmd := exec.Command("git", "ls-remote", l.sshURL())
		cmd.Env = append(os.Environ(),
			"GIT_SSH_COMMAND=ssh -i "+path+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o BatchMode=yes",
			"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		return cmd.Run() == nil
	})
}

// git runs git in the working copy, over SSH with the test's own key and no prompts.
func (l *buildLab) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = l.work
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+l.keyPath+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o BatchMode=yes",
		"GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=Gate Test", "GIT_AUTHOR_EMAIL=gate@example.com",
		"GIT_COMMITTER_NAME=Gate Test", "GIT_COMMITTER_EMAIL=gate@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes the Dockerfile as the repository's, commits it and returns the commit's SHA.
func (l *buildLab) commit(t *testing.T, dockerfile, subject string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(l.work, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	l.git(t, "add", "Dockerfile")
	l.git(t, "commit", "-q", "-m", subject)
	return l.git(t, "rev-parse", "HEAD")
}

// push sends HEAD to the branch of the Git server and waits for the HTTP side to list it too
// (smart HTTP reads the repository itself, so it is the same moment).
func (l *buildLab) push(t *testing.T, branch string) {
	t.Helper()
	l.git(t, "push", "-q", "--force", "origin", "HEAD:refs/heads/"+branch)
	sha := l.git(t, "rev-parse", "HEAD")
	waitFor(t, "the Git server's HTTP side to list "+sha[:7], 30*time.Second, func() bool {
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/cgi-bin/git/app.git/info/refs?service=git-upload-pack", l.httpPort))
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return strings.Contains(string(raw), sha+" refs/heads/"+branch)
	})
}

// hostKey reads the Git server's ed25519 host key as the app's build.hostKey wants it:
// "<type> <base64>". The handshake is stopped at the key: no login is tried.
func (l *buildLab) hostKey(t *testing.T) string {
	t.Helper()
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "git",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return fmt.Errorf("host key read")
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           10 * time.Second,
	}
	_, _ = ssh.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", l.sshPort), cfg)
	if got == nil {
		t.Fatalf("could not read the Git server's host key on port %d", l.sshPort)
	}
	return got.Type() + " " + base64.StdEncoding.EncodeToString(got.Marshal())
}

// labDockerfile is the app of the build steps: busybox's httpd answering `version` at /.
func labDockerfile(version string) string {
	return "FROM busybox:1.37\nRUN mkdir -p /www && echo " + version + " > /www/index.html\n" +
		"CMD [\"httpd\", \"-f\", \"-p\", \"8080\", \"-h\", \"/www\"]\n"
}

// labBrokenDockerfile fails its build at a RUN.
const labBrokenDockerfile = "FROM busybox:1.37\nRUN echo gate-build-broken-line && exit 1\n"

// buildApp is the body of deployApp for an app with one built service.
func buildAppBody(image, host string, build map[string]any) string {
	body, _ := json.Marshal(map[string]any{
		"source": map[string]any{"kind": "image"},
		"services": []map[string]any{{
			"name":      "web",
			"image":     image,
			"ports":     []map[string]any{{"name": "http", "containerPort": 8080}},
			"domains":   []map[string]any{{"host": host, "port": "http"}},
			"health":    map[string]any{"path": "/", "interval": "5s", "timeout": "2s"},
			"resources": labResources,
			"build":     build,
		}},
	})
	return string(body)
}
