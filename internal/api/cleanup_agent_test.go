package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/cosmic-chimps/caravana/internal/nodeagent"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// spaceHost is the fake host of the other agent tests with a Docker that holds three images: one a
// container uses and two that nobody does. It remembers the images it was told to remove.
type spaceHost struct {
	fakeHost
	mu      sync.Mutex
	removed []string
	dfCalls int
	// volumes and activeVolumes are Docker's local volumes and how many a container uses; the leftovers
	// are the difference. pruned counts the "docker volume prune -f" calls, which drop the leftovers.
	volumes, activeVolumes int
	pruned                 int
}

const (
	imgUsed  = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	imgOld   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	imgOther = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func (h *spaceHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if name != "docker" || len(args) == 0 || args[0] == "info" {
		return h.fakeHost.Run(ctx, name, args...)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	line := func(id, tag string, size int) string {
		return fmt.Sprintf(`{"Id":%q,"RepoDigests":["%s@%s"],"RepoTags":[%q],"Size":%d}`, id, tag[:strings.Index(tag, ":")], strings.Replace(id, "sha256:", "sha256:a", 1)[:71], tag, size)
	}
	switch strings.Join(args[:2], " ") {
	case "image ls":
		return []byte(imgUsed + "\n" + imgOld + "\n" + imgOther + "\n"), nil
	case "image inspect":
		return []byte(line(imgUsed, "web:1", 5_000_000) + "\n" + line(imgOld, "old:1", 300_000_000) + "\n" + line(imgOther, "other:2", 100_000_000) + "\n"), nil
	case "container ls":
		return []byte("aaaaaaaaaaaa\n"), nil
	case "container inspect":
		return []byte(imgUsed + "\n"), nil
	case "system df":
		h.dfCalls++
		images := "405.0MB"
		if len(h.removed) > 0 {
			images = "5.0MB"
		}
		return []byte(`{"Type":"Images","TotalCount":"3","Active":"1","Size":"` + images + `","Reclaimable":"0B"}` + "\n" +
			fmt.Sprintf(`{"Type":"Local Volumes","TotalCount":"%d","Active":"%d","Size":"0B","Reclaimable":"0B"}`, h.volumes, h.activeVolumes) + "\n" +
			`{"Type":"Build Cache","TotalCount":"0","Active":"0","Size":"0B","Reclaimable":"0B"}` + "\n"), nil
	case "image rm":
		h.removed = append(h.removed, strings.Join(args[2:], " "))
		return nil, nil
	case "volume prune":
		if strings.Join(args, " ") != "volume prune -f" {
			return nil, fmt.Errorf("unexpected docker call %v", args)
		}
		h.pruned++
		h.volumes = h.activeVolumes
		return nil, nil
	case "builder prune":
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected docker call %v", args)
}

// setVolumes changes Docker's local volumes while an agent is running.
func (h *spaceHost) setVolumes(total, active int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.volumes, h.activeVolumes = total, active
}

// A clean-up end to end, with nothing in the middle: the real agent reports what Docker holds, the
// console is asked to clean up, hands the request out in the sync answer, the real agent runs it and
// reports how it went, and the console settles it. Both sides' JSON is checked against each other.
func TestACleanUpThroughTheRealAgent(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "fsn-e2e"}`)

	host := &spaceHost{fakeHost: fakeHost{hostname: "fsn-e2e.example.com"}}
	var stdout, stderr bytes.Buffer
	root := t.TempDir()
	agent := &nodeagent.Agent{
		Root: root, Runner: host, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL, Stdout: &stdout, Stderr: &stderr,
	}
	ctx := context.Background()
	if err := agent.Join(ctx, nodeagent.JoinOptions{URL: e.RawURL, Token: j.Token, Address: "203.0.113.9"}); err != nil {
		t.Fatalf("Join: %v\n%s", err, stderr.String())
	}
	e.Nomad.AddNode("node-2", "fsn-e2e", "ready", "10.99.0.2")
	for range 2 { // the first sync finds no Nomad node, the second marks it ready
		if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil {
			t.Fatalf("Sync: %v\n%s", err, stderr.String())
		}
	}

	// The report carried what Docker holds: the space read has the images, two of them unused.
	v, err := e.Service.ServerSpace(ctx, "fsn-e2e")
	if err != nil || v.Images == nil || v.Images.Count != 3 || v.Images.UnusedCount != 2 || v.Images.UnusedBytes != 400_000_000 {
		t.Fatalf("space = %+v, %v", v.Images, err)
	}

	c, err := e.Service.RequestCleanup(ctx, "fsn-e2e", store.CleanupByOwner, false)
	if err != nil {
		t.Fatalf("RequestCleanup: %v", err)
	}
	stdout.Reset()
	if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil { // handed out and run
		t.Fatalf("Sync: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Cleaned up: removed 2 images, freed 400.0 MB") {
		t.Errorf("the agent printed %q", stdout.String())
	}
	if len(host.removed) != 2 || !strings.Contains(strings.Join(host.removed, "|"), "old:1") || !strings.Contains(strings.Join(host.removed, "|"), "other:2") ||
		strings.Contains(strings.Join(host.removed, "|"), "web:1") {
		t.Errorf("removed = %v", host.removed)
	}
	if got, _ := e.Service.ServerCleanup(ctx, "fsn-e2e", c.ID); got.Status != store.CleanupRunning {
		t.Errorf("after the hand-out the clean-up is %s", got.Status)
	}

	if err := agent.Sync(ctx, nodeagent.SyncOptions{}); err != nil { // reports the outcome
		t.Fatalf("Sync: %v", err)
	}
	got, err := e.Service.ServerCleanup(ctx, "fsn-e2e", c.ID)
	if err != nil || got.Status != store.CleanupDone || got.ImagesRemoved != 2 || got.BytesFreed != 400_000_000 || got.Error != "" {
		t.Errorf("settled = %+v, %v", got, err)
	}
	if n := len(host.removed); n != 2 {
		t.Errorf("the same request ran again: %d removals", n)
	}
}

// A sync's body may name a thousand images, and a bad block is refused naming its field; the other node
// calls keep their 64 KiB.
func TestSyncBodyAndSpaceRules(t *testing.T) {
	e := newTestEnv(t, "")
	withCluster(t, e)
	j := addServer(t, e, `{"name": "fsn-e2e"}`)
	root := t.TempDir()
	agent := &nodeagent.Agent{
		Root: root, Runner: &fakeHost{hostname: "fsn-e2e.example.com"}, HTTP: nodeagent.NewHTTPClient(false),
		Version: "0.0.1-e2e", NomadAddr: localNomad(t).URL,
	}
	if err := agent.Join(context.Background(), nodeagent.JoinOptions{URL: e.RawURL, Token: j.Token, Address: "203.0.113.9"}); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(root, "etc/caravana-node/node.env"))
	auth := "Bearer " + regexp.MustCompile(`cvnd_[A-Za-z0-9_-]{43}`).FindString(string(env))

	report := func(images []map[string]any) string {
		b, _ := json.Marshal(map[string]any{
			"agent": "0.0.1", "nomad": "2.0.7", "docker": "27.5.1", "dockerRunning": true, "diskFreeMb": 1, "diskTotalMb": 2,
			"memoryMb": 3, "kernel": "6", "role": "client", "nomadLeader": true, "serverRunning": false, "reach": []any{},
			"space": map[string]any{"images": images, "imagesTruncated": false, "dockerVolumes": map[string]any{"count": 0, "bytes": 0},
				"buildCacheBytes": 0, "volumes": []any{}},
		})
		return string(b)
	}
	tags := make([]string, 16)
	for i := range tags {
		tags[i] = strings.Repeat("t", 250)
	}
	var many []map[string]any
	for i := range 300 { // about 1.3 MB
		many = append(many, map[string]any{"id": fmt.Sprintf("sha256:%064x", i), "digests": []string{}, "tags": tags, "bytes": 1, "inUse": false})
	}
	body := report(many)
	if len(body) < 1<<20 {
		t.Fatalf("the test body is only %d bytes", len(body))
	}
	if status, _, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/node/sync", "application/json", body); status != http.StatusOK {
		t.Errorf("a report of %d bytes = %d %.200s", len(body), status, raw)
	}

	bad := report([]map[string]any{{"id": "x", "digests": []string{"nope"}, "tags": []string{}, "bytes": 1, "inUse": false}})
	status, _, raw := callWith(t, auth, http.MethodPost, e.RawURL+"/v1/node/sync", "application/json", bad)
	wantError(t, "a bad digest", status, raw, http.StatusUnprocessableEntity, codeInvalidField, "space.images[0].digests[0]")

	// The certificate call keeps its small limit.
	huge := `{"csr": "` + strings.Repeat("A", 70<<10) + `"}`
	status, _, raw = callWith(t, auth, http.MethodPost, e.RawURL+"/v1/node/certificate", "application/json", huge)
	wantError(t, "a big certificate request", status, raw, http.StatusRequestEntityTooLarge, codeBodyTooLarge, "")
}
