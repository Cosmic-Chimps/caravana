package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Docker outputs under testdata/docker were recorded on the lab VM (spec 045, research R4): 7 images,
// 3 of them used by containers, 154 leftover volumes, a build cache of 0.

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "docker", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeDocker answers Docker's calls from the recorded outputs and remembers the ones that change things.
type fakeDocker struct {
	t  *testing.T
	mu sync.Mutex

	imageLS, imageInspect, containerLS, containerInspect string
	// df holds the answers of successive `system df` calls; the last one repeats.
	df      []string
	dfCalls int

	calls [][]string
	// rmErr, when set, makes `image rm` of an image named in failOn fail.
	rmErr  error
	failOn string
	// onRM is called before each `image rm`.
	onRM func(args []string)
	// vanishOn, when set, makes `image rm` of an image named in it fail with vanishErr after removing the
	// image: a later `image inspect` of its ID says "No such image" (Docker 29 removes an image's other
	// names with the first).
	vanishOn  string
	vanishErr error
	gone      map[string]bool
}

func newFakeDocker(t *testing.T) *fakeDocker {
	return &fakeDocker{
		t: t, imageLS: fixture(t, "image-ls.txt"), imageInspect: fixture(t, "image-inspect.txt"),
		containerLS: fixture(t, "container-ls.txt"), containerInspect: fixture(t, "container-inspect.txt"),
		df: []string{fixture(t, "system-df.txt")},
	}
}

func (d *fakeDocker) Run(args []string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(args) >= 2 {
		switch args[0] + " " + args[1] {
		case "image ls":
			return []byte(d.imageLS), nil
		case "image inspect":
			// Only the images asked for, as Docker answers.
			var out []string
			for _, line := range strings.Split(d.imageInspect, "\n") {
				for _, id := range args[4:] {
					if strings.Contains(line, `"Id":"`+id+`"`) {
						out = append(out, line)
					}
				}
			}
			if len(out) == 0 || len(args) > 4 && d.gone[args[len(args)-1]] {
				return nil, errors.New("docker image failed: exit status 1: Error response from daemon: No such image: " + args[len(args)-1])
			}
			return []byte(strings.Join(out, "\n") + "\n"), nil
		case "container ls":
			return []byte(d.containerLS), nil
		case "container inspect":
			return []byte(d.containerInspect), nil
		case "system df":
			out := d.df[min(d.dfCalls, len(d.df)-1)]
			d.dfCalls++
			return []byte(out), nil
		case "image rm":
			d.calls = append(d.calls, append([]string(nil), args...))
			if d.onRM != nil {
				d.onRM(args)
			}
			if d.rmErr != nil && strings.Contains(strings.Join(args, " "), d.failOn) {
				return nil, d.rmErr
			}
			if d.vanishErr != nil && strings.Contains(strings.Join(args, " "), d.vanishOn) {
				for _, line := range strings.Split(d.imageInspect, "\n") {
					if strings.Contains(line, `"`+args[2]+`"`) {
						var in struct {
							ID string `json:"Id"`
						}
						if json.Unmarshal([]byte(line), &in) == nil {
							if d.gone == nil {
								d.gone = map[string]bool{}
							}
							d.gone[in.ID] = true
						}
					}
				}
				return nil, d.vanishErr
			}
			return nil, nil
		case "builder prune", "volume prune":
			d.calls = append(d.calls, append([]string(nil), args...))
			return nil, nil
		}
	}
	d.t.Errorf("unexpected docker call %v", args)
	return nil, errors.New("unexpected")
}

// changing is what the fake saw of calls that change Docker, one per entry.
func (d *fakeDocker) changing() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, c := range d.calls {
		out = append(out, strings.Join(c, " "))
	}
	return out
}

// dfAfter is the recorded `system df` after a clean-up that freed 300 MB of images and all 91.05 MB of
// the leftover volumes.
func dfAfter(t *testing.T) string {
	s := fixture(t, "system-df.txt")
	s = strings.Replace(s, `"Size":"1.472GB"`, `"Size":"1.172GB"`, 1)
	s = strings.Replace(s, `"Size":"91.05MB","TotalCount":"154"`, `"Size":"0B","TotalCount":"0"`, 1)
	return s
}

// withDocker makes the agent's runner answer Docker from d.
func withDocker(a *testAgent, d *fakeDocker) { a.runner.docker = d.Run }

func imageByTag(t *testing.T, images []SpaceImage, tag string) SpaceImage {
	t.Helper()
	for _, img := range images {
		if slices.Contains(img.Tags, tag) {
			return img
		}
	}
	t.Fatalf("no image tagged %s", tag)
	return SpaceImage{}
}

func TestParseHumanSize(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0B", 0, true},
		{"8.192kB", 8192, true},
		{"65.54kB", 65540, true},
		{"91.05MB", 91050000, true},
		{"326.5MB", 326500000, true},
		{"1.472GB", 1472000000, true},
		{"2TB", 2e12, true},
		{" 12 MB ", 12e6, true},
		{"1.5GiB", 1610612736, true},
		{"", 0, false},
		{"MB", 0, false},
		{"12", 0, false},
		{"12 parsecs", 0, false},
		{"-5MB", 0, false},
		{"9999999PB", 0, false},
	}
	for _, tt := range tests {
		got, err := parseHumanSize(tt.in)
		if (err == nil) != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("parseHumanSize(%q) = %d, %v; want %d ok=%v", tt.in, got, err, tt.want, tt.ok)
		}
	}
}

func TestParseDockerDF(t *testing.T) {
	df, err := parseDockerDF(fixture(t, "system-df.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := dockerDF{
		"Images":        {Total: 7, Active: 3, Size: 1472000000, Reclaimable: 326500000},
		"Containers":    {Total: 7, Active: 5, Size: 65540, Reclaimable: 8192},
		"Local Volumes": {Total: 154, Active: 0, Size: 91050000, Reclaimable: 91050000},
		"Build Cache":   {},
	}
	for typ, w := range want {
		if df[typ] != w {
			t.Errorf("%s = %+v, want %+v", typ, df[typ], w)
		}
	}

	// A Docker that prints numbers reads the same.
	df, err = parseDockerDF(`{"Type":"Images","TotalCount":7,"Active":3,"Size":"1.472GB","Reclaimable":"326.5MB (22%)"}`)
	if err != nil || df["Images"].Total != 7 || df["Images"].Active != 3 {
		t.Errorf("numbers: %+v, %v", df, err)
	}

	for _, bad := range []string{"", "27.5.1\n", `{"Type":"Images","TotalCount":"x","Active":"1","Size":"1MB","Reclaimable":"0B"}`,
		`{"Type":"Images","TotalCount":"1","Active":"1","Size":"big","Reclaimable":"0B"}`} {
		if _, err := parseDockerDF(bad); err == nil {
			t.Errorf("parseDockerDF(%q) accepted it", bad)
		}
	}
}

func TestInventoryFromRecordedDocker(t *testing.T) {
	a := newTestAgent(t, nil)
	withDocker(a, newFakeDocker(t))
	images, err := a.inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 7 {
		t.Fatalf("%d images, want 7", len(images))
	}
	inUse := map[string]bool{}
	for _, img := range images {
		if img.InUse {
			inUse[img.ID[:19]] = true
		}
	}
	// The whoami, pause and traefik images are the ones containers use; the container of an image that
	// isn't listed any more (sha256:3103…) changes nothing.
	for _, id := range []string{"sha256:c4717a8d1f01", "sha256:9c4504dfea56", "sha256:24841fe2de73"} {
		if !inUse[id] {
			t.Errorf("%s isn't in use", id)
		}
	}
	if len(inUse) != 3 {
		t.Errorf("%d images in use, want 3: %v", len(inUse), inUse)
	}

	node := imageByTag(t, images, "node:24-alpine")
	if node.Bytes != 62283690 || len(node.Digests) != 1 || !digestRE.MatchString(node.Digests[0]) || node.InUse {
		t.Errorf("node = %+v", node)
	}
	if len(node.refs) != 2 || node.refs[0] != "node:24-alpine" || !strings.HasPrefix(node.refs[1], "node@sha256:") {
		t.Errorf("node refs = %v: every tag and every repo@digest are what an image is removed by", node.refs)
	}
	for _, img := range images {
		for _, ref := range img.refs {
			if ref == "" || strings.HasPrefix(ref, "-") {
				t.Errorf("a ref the removal would misread: %q", ref)
			}
		}
	}
}

// A container list or an inspect Docker can't answer must not read as "no container uses anything".
func TestInventoryRefusesWhenContainersCantBeRead(t *testing.T) {
	for _, call := range []string{"container ls", "container inspect", "image ls"} {
		d := newFakeDocker(t)
		a := newTestAgent(t, nil)
		a.runner.docker = func(args []string) ([]byte, error) {
			if args[0]+" "+args[1] == call {
				return nil, errors.New("docker: boom")
			}
			return d.Run(args)
		}
		if _, err := a.inventory(context.Background()); err == nil {
			t.Errorf("an inventory without %q was returned", call)
		}
	}
	// No container at all is an empty list, not an error, and runs no inspect.
	d := newFakeDocker(t)
	d.containerLS = ""
	a := newTestAgent(t, nil)
	withDocker(a, d)
	images, err := a.inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range images {
		if img.InUse {
			t.Errorf("%s in use with no container", img.ID)
		}
	}
}

func TestParseImageInspectSkipsWhatItCannotRead(t *testing.T) {
	good := `{"Id":"sha256:` + strings.Repeat("a", 64) + `","RepoDigests":["x/y@sha256:` + strings.Repeat("b", 64) + `","bad"],"RepoTags":["x/y:1","<none>:<none>"],"Size":5}`
	got := parseImageInspect([]byte("\n" + good + "\nnot json\n{\"Id\":\"nope\"}\nError response from daemon: No such image: z\n"))
	if len(got) != 1 {
		t.Fatalf("%d images, want 1", len(got))
	}
	img := got[0]
	if len(img.Digests) != 1 || img.Digests[0] != "sha256:"+strings.Repeat("b", 64) || len(img.Tags) != 1 || img.Tags[0] != "x/y:1" || img.Bytes != 5 {
		t.Errorf("image = %+v", img)
	}
	if len(img.refs) != 2 || img.refs[0] != "x/y:1" || img.refs[1] != "x/y@sha256:"+strings.Repeat("b", 64) {
		t.Errorf("refs = %v: a digest that isn't repo@sha256:… is no name to remove an image by", img.refs)
	}
}

func TestSpaceReport(t *testing.T) {
	a := newTestAgent(t, nil)
	withDocker(a, newFakeDocker(t))
	st := State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleServer,
		VolumeSizes:       map[string]int64{"e971d06a-1f46-fc04-583b-25618814daeb": 2300000000, "bad name": 5, "z": 1},
		VolumesMeasuredAt: "2026-11-20T03:35:20Z",
		LastClean:         &CleanOutcome{ID: strings.Repeat("a", 32), StartedAt: "2026-11-20T04:00:00Z"}}
	r := a.space(context.Background(), st)
	if r == nil {
		t.Fatal("no space block")
	}
	if len(r.Images) != 7 || r.ImagesTruncated {
		t.Errorf("%d images, truncated %v", len(r.Images), r.ImagesTruncated)
	}
	for i := 1; i < len(r.Images); i++ {
		if r.Images[i-1].Bytes < r.Images[i].Bytes {
			t.Error("the images aren't the largest first")
		}
	}
	if r.DockerVolumes != (DockerVolumes{Count: 154, Bytes: 91050000}) || r.BuildCacheBytes != 0 {
		t.Errorf("docker volumes %+v, build cache %d", r.DockerVolumes, r.BuildCacheBytes)
	}
	if len(r.Volumes) != 2 || r.Volumes[0].ID != "e971d06a-1f46-fc04-583b-25618814daeb" || r.Volumes[0].Bytes != 2300000000 || r.Volumes[1].ID != "z" {
		t.Errorf("volumes = %+v: a name the console would refuse is left out", r.Volumes)
	}
	if r.VolumesMeasuredAt != "2026-11-20T03:35:20Z" || r.LastClean == nil || r.LastClean.FinishedAt != "" {
		t.Errorf("measured %q, lastClean %+v: a clean-up with no end is reported as it is", r.VolumesMeasuredAt, r.LastClean)
	}
	b, _ := json.Marshal(r)
	for _, f := range []string{"images", "imagesTruncated", "dockerVolumes", "buildCacheBytes", "volumes", "volumesMeasuredAt", "lastClean", "inUse", "digests", "tags"} {
		if !strings.Contains(string(b), `"`+f+`"`) {
			t.Errorf("the space block has no %q", f)
		}
	}
	if strings.Contains(string(b), `"refs"`) {
		t.Error("the names an image is removed by are sent")
	}
}

func TestSpaceReportTruncatesToTheLargest(t *testing.T) {
	var lines []string
	var ids []string
	for i := range 1005 {
		id := fmt.Sprintf("sha256:%064x", i+1)
		ids = append(ids, id)
		lines = append(lines, fmt.Sprintf(`{"Id":%q,"RepoDigests":[],"RepoTags":[],"Size":%d}`, id, i+1))
	}
	d := newFakeDocker(t)
	d.imageLS, d.imageInspect, d.containerLS = strings.Join(ids, "\n")+"\n", strings.Join(lines, "\n")+"\n", ""
	a := newTestAgent(t, nil)
	withDocker(a, d)
	r := a.space(context.Background(), State{})
	if r == nil || !r.ImagesTruncated || len(r.Images) != maxSpaceImages {
		t.Fatalf("report = %+v", r)
	}
	if r.Images[0].Bytes != 1005 || r.Images[maxSpaceImages-1].Bytes != 6 {
		t.Errorf("kept sizes %d down to %d, want the 1000 largest", r.Images[0].Bytes, r.Images[maxSpaceImages-1].Bytes)
	}
	for _, img := range r.Images {
		if img.Digests == nil || img.Tags == nil {
			t.Fatal("an image with null digests or tags")
		}
	}
}

// A report that would weigh more than the console takes loses its smallest images.
func TestSpaceReportStaysUnderTheLimit(t *testing.T) {
	long := strings.Repeat("t", 290)
	var ids, lines []string
	for i := range 1000 {
		id := fmt.Sprintf("sha256:%064x", i+1)
		ids = append(ids, id)
		var tags, digests []string
		for j := range 16 {
			tags = append(tags, fmt.Sprintf("%s%d", long, j))
			digests = append(digests, fmt.Sprintf("r@sha256:%064x", i*16+j+1))
		}
		tj, _ := json.Marshal(tags)
		dj, _ := json.Marshal(digests)
		lines = append(lines, fmt.Sprintf(`{"Id":%q,"RepoDigests":%s,"RepoTags":%s,"Size":%d}`, id, dj, tj, i+1))
	}
	d := newFakeDocker(t)
	d.imageLS, d.imageInspect, d.containerLS = strings.Join(ids, "\n")+"\n", strings.Join(lines, "\n")+"\n", ""
	a := newTestAgent(t, nil)
	withDocker(a, d)
	r := a.space(context.Background(), State{})
	if r == nil || !r.ImagesTruncated || len(r.Images) == 0 || len(r.Images) >= 1000 {
		t.Fatalf("images %d, truncated %v", len(r.Images), r.ImagesTruncated)
	}
	if b, _ := json.Marshal(r); len(b) > maxSpaceJSON {
		t.Errorf("the block is %d bytes", len(b))
	}
	if r.Images[0].Bytes != 1000 {
		t.Errorf("the largest image isn't first: %d", r.Images[0].Bytes)
	}
}

func TestReportHasNoSpaceWhenDockerDoesNotAnswer(t *testing.T) {
	a := newTestAgent(t, nil)
	a.runner.docker = func(args []string) ([]byte, error) { return nil, errors.New("Cannot connect to the Docker daemon") }
	r := a.Report(context.Background(), State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleClient})
	if r.Space != nil {
		t.Errorf("space = %+v", r.Space)
	}
	if b, _ := json.Marshal(r); strings.Contains(string(b), `"space"`) {
		t.Errorf("the report names space: %s", b)
	}
	// And the plain fake, which answers every docker call with a version, gives none either.
	if r := newTestAgent(t, nil).Report(context.Background(), State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleClient}); r.Space != nil {
		t.Errorf("space from a Docker that only says its version: %+v", r.Space)
	}
}

func TestRemovable(t *testing.T) {
	d := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	img := func(id string, inUse bool, digests ...string) SpaceImage {
		return SpaceImage{ID: "sha256:" + strings.Repeat(id, 64), InUse: inUse, Digests: digests}
	}
	images := []SpaceImage{
		img("1", false, d("a")),         // unused, not kept: goes
		img("2", false, d("b")),         // unused, kept
		img("3", true, d("c")),          // used: stays
		img("4", false),                 // unused with no digest: goes
		img("5", false, d("d"), d("b")), // two digests, one kept: stays
		img("6", true),                  // used with no digest: stays
		img("7", false, d("e"), d("f")), // two digests, none kept: goes
		img("8", true, d("g")),          // used and kept
	}
	ids := func(l []SpaceImage) string {
		var out []string
		for _, i := range l {
			out = append(out, i.ID[7:8])
		}
		return strings.Join(out, "")
	}
	tests := []struct {
		name string
		keep []string
		want string
	}{
		{"keep b", []string{d("b")}, "147"},
		{"keep nothing", nil, "12457"},
		{"keep everything", []string{d("a"), d("b"), d("d"), d("e")}, "4"},
		{"a digest that matches nothing", []string{d("9")}, "12457"},
	}
	for _, tt := range tests {
		if got := ids(removable(images, tt.keep)); got != tt.want {
			t.Errorf("%s: removes %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := removable(nil, nil); len(got) != 0 {
		t.Errorf("removable(nil) = %v", got)
	}
}

func TestMeasureVolumes(t *testing.T) {
	a := newTestAgent(t, nil)
	ctx := context.Background()
	st := State{Version: 1, Index: 1, Name: "one", Pool: "default", Role: RoleServer}

	// A host with no host_volumes folder has nothing to measure.
	if _, changed := a.measureVolumes(ctx, st, false); changed {
		t.Error("measured a folder that isn't there")
	}

	vols := []string{"e971d06a-1f46-fc04-583b-25618814daeb", "aaaa-bbbb"}
	for _, v := range vols {
		if err := os.MkdirAll(a.file("opt/nomad/data/host_volumes/"+v), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// What the folder holds that isn't a volume: a file, a name `du` must never be given.
	a.writeFiles(t, map[string]string{"opt/nomad/data/host_volumes/stray.txt": "x"})
	if err := os.MkdirAll(a.file("opt/nomad/data/host_volumes/bad name"), 0o755); err != nil {
		t.Fatal(err)
	}
	var asked []string
	sizes := map[string]string{"e971d06a-1f46-fc04-583b-25618814daeb": "2246094", "aaaa-bbbb": "12"}
	a.runner.du = func(args []string) ([]byte, error) {
		asked = append(asked, strings.Join(args, " "))
		path := args[len(args)-1]
		return []byte(sizes[filepath.Base(path)] + "\t" + path + "\n"), nil
	}

	now := t0
	a.Now = func() time.Time { return now }
	got, changed := a.measureVolumes(ctx, st, false)
	if !changed || got.VolumeSizes["e971d06a-1f46-fc04-583b-25618814daeb"] != 2246094*1024 || got.VolumeSizes["aaaa-bbbb"] != 12*1024 || len(got.VolumeSizes) != 2 {
		t.Fatalf("sizes = %v changed %v", got.VolumeSizes, changed)
	}
	if got.VolumesMeasuredAt != t0.Format(time.RFC3339) {
		t.Errorf("measured at %q", got.VolumesMeasuredAt)
	}
	if len(asked) != 2 {
		t.Fatalf("du ran %d times: %v", len(asked), asked)
	}
	for _, c := range asked {
		if !strings.HasPrefix(c, "-sk "+a.file("opt/nomad/data/host_volumes")+"/") {
			t.Errorf("du %s", c)
		}
	}

	// Fresh sizes are kept for fifteen minutes, then measured again, and force measures at once.
	now = t0.Add(14 * time.Minute)
	if _, changed := a.measureVolumes(ctx, got, false); changed {
		t.Error("measured again after 14 minutes")
	}
	if _, changed := a.measureVolumes(ctx, got, true); !changed {
		t.Error("force didn't measure")
	}
	now = t0.Add(16 * time.Minute)
	sizes["aaaa-bbbb"] = "40"
	// A directory du can't read keeps its last size.
	a.runner.du = func(args []string) ([]byte, error) {
		path := args[len(args)-1]
		if filepath.Base(path) == "e971d06a-1f46-fc04-583b-25618814daeb" {
			return nil, errors.New("du: boom")
		}
		return []byte("40\t" + path + "\n"), nil
	}
	again, changed := a.measureVolumes(ctx, got, false)
	if !changed || again.VolumeSizes["aaaa-bbbb"] != 40*1024 || again.VolumeSizes["e971d06a-1f46-fc04-583b-25618814daeb"] != 2246094*1024 {
		t.Errorf("after 16 minutes: %v", again.VolumeSizes)
	}
}

// The sizes are measured by sync, kept in state.json and carried by every report, and a sync that
// measures nothing leaves the state alone (the "nothing changed" check of the other sync tests).
func TestSyncKeepsTheVolumeSizesInState(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	c.on("/v1/node/sync", a.syncAnswer(nil))
	vol := "e971d06a-1f46-fc04-583b-25618814daeb"
	if err := os.MkdirAll(a.file("opt/nomad/data/host_volumes/"+vol), 0o755); err != nil {
		t.Fatal(err)
	}
	d := newFakeDocker(t)
	withDocker(a, d)
	runs := 0
	a.runner.du = func(args []string) ([]byte, error) {
		runs++
		return []byte("1000\t" + args[len(args)-1] + "\n"), nil
	}
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	st, err := a.LoadState()
	if err != nil || st.VolumeSizes[vol] != 1000*1024 || st.VolumesMeasuredAt == "" {
		t.Fatalf("state = %+v, %v", st, err)
	}
	space, _ := c.calls("/v1/node/sync")[0].body["space"].(map[string]any)
	vols, _ := space["volumes"].([]any)
	if len(vols) != 1 || vols[0].(map[string]any)["bytes"].(float64) != 1000*1024 {
		t.Errorf("the report's volumes = %v", space["volumes"])
	}
	// The next sync is within the fifteen minutes: du doesn't run, the sizes still go out.
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Errorf("du ran %d times", runs)
	}
	space, _ = c.calls("/v1/node/sync")[1].body["space"].(map[string]any)
	if vols, _ := space["volumes"].([]any); len(vols) != 1 {
		t.Errorf("the second report's volumes = %v", space["volumes"])
	}
}

func TestParseSyncCleanRules(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	id := strings.Repeat("0123456789abcdef", 2)
	many := make([]string, maxKeep+1)
	for i := range many {
		many[i] = digest
	}
	tests := []struct {
		name  string
		clean any
		field string // "" means accepted
	}{
		{"absent", nil, ""},
		{"valid", map[string]any{"id": id, "keep": []string{digest}, "dockerVolumes": true}, ""},
		{"no keep list", map[string]any{"id": id}, ""},
		{"4096 digests", map[string]any{"id": id, "keep": many[:maxKeep]}, ""},
		{"4097 digests", map[string]any{"id": id, "keep": many}, "clean.keep"},
		{"id of 31", map[string]any{"id": id[:31]}, "clean.id"},
		{"id upper case", map[string]any{"id": strings.ToUpper(id)}, "clean.id"},
		{"id missing", map[string]any{"keep": []string{digest}}, "clean.id"},
		{"id not hex", map[string]any{"id": strings.Repeat("z", 32)}, "clean.id"},
		{"digest without prefix", map[string]any{"id": id, "keep": []string{strings.Repeat("a", 64)}}, "clean.keep[0]"},
		{"digest short", map[string]any{"id": id, "keep": []string{digest, digest[:30]}}, "clean.keep[1]"},
		{"digest upper case", map[string]any{"id": id, "keep": []string{"sha256:" + strings.Repeat("A", 64)}}, "clean.keep[0]"},
		{"digest with an option in it", map[string]any{"id": id, "keep": []string{"--force"}}, "clean.keep[0]"},
		{"dockerVolumes a string", map[string]any{"id": id, "dockerVolumes": "yes"}, "clean.dockerVolumes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := syncAnswer()
			if tt.clean != nil {
				m["clean"] = tt.clean
			}
			_, err := ParseSync(asJSON(t, m), Expect{State: syncState(), OwnPublicKey: ownKey, Now: t0})
			if tt.field == "" {
				if err != nil {
					t.Fatalf("refused a valid answer: %v", err)
				}
				return
			}
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tt.field {
				t.Fatalf("err = %v, want a FieldError for %s", err, tt.field)
			}
			if strings.Contains(err.Error(), "--force") {
				t.Errorf("the error quotes the digest: %v", err)
			}
		})
	}

	// 4096 digests fit in the answer the agent reads: the body is about 300 KiB.
	m := syncAnswer()
	m["clean"] = map[string]any{"id": id, "keep": many[:maxKeep]}
	if body := asJSON(t, m); len(body) < 256<<10 || len(body) > maxAnswer {
		t.Errorf("the largest valid answer is %d bytes, maxAnswer %d", len(body), maxAnswer)
	}
}

// ---- the clean-up -------------------------------------------------------------------------------

const cleanIDA = "a3f9a3f9a3f9a3f9a3f9a3f9a3f9a3f9"

// cleanAnswer is a sync answer with a clean block.
func (a *testAgent) cleanAnswer(id string, keep []string, dockerVolumes bool) func(map[string]any, string) (int, any) {
	return a.syncAnswer(func(m map[string]any) {
		m["clean"] = map[string]any{"id": id, "keep": keep, "dockerVolumes": dockerVolumes}
	})
}

// A clean-up runs once per ID: the whoami image is used, the postgres one is kept by its digest, the
// others go by every name they have; the build cache is pruned and the leftover volumes are not.
func TestSyncRunsTheCleanUpOncePerID(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	d.df = []string{fixture(t, "system-df.txt"), fixture(t, "system-df.txt"), dfAfter(t)} // report, before, after, ...
	withDocker(a, d)

	images, err := a.inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pg := imageByTag(t, images, "postgres@sha256:2d2b8998d31037bf721cfdf764d76ba74171b4fab3431b7f72c27c56ddbdf9e3")
	c.on("/v1/node/sync", a.cleanAnswer(cleanIDA, pg.Digests, false))

	// The state written first must already name the ID when the first image is removed.
	d.onRM = func(args []string) {
		st, err := a.LoadState()
		if err != nil || st.LastClean == nil || st.LastClean.ID != cleanIDA || st.LastClean.FinishedAt != "" {
			t.Errorf("state when the first image goes: %+v, %v", st.LastClean, err)
		}
		d.onRM = nil
	}
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}

	got := d.changing()
	if len(got) != 4 { // three images, the build cache
		t.Fatalf("docker changed things %d times: %v", len(got), got)
	}
	for _, line := range got[:3] {
		if !strings.HasPrefix(line, "image rm ") || strings.Contains(line, "--force") || strings.Contains(line, " -f") {
			t.Errorf("removal %q", line)
		}
	}
	joined := strings.Join(got, "\n")
	for _, must := range []string{"image rm node:24-alpine node@sha256:", "aquasec/trivy:0.69.3", "alpine:3.14.8"} {
		if !strings.Contains(joined, must) {
			t.Errorf("no removal with %q:\n%s", must, joined)
		}
	}
	for _, mustNot := range []string{"postgres", "traefik", "whoami", "pause"} {
		if strings.Contains(joined, mustNot) {
			t.Errorf("a removal names %q:\n%s", mustNot, joined)
		}
	}
	if got[3] != "builder prune -af" {
		t.Errorf("last call %q", got[3])
	}
	if strings.Contains(joined, "volume prune") {
		t.Error("Docker's volumes were pruned without being asked")
	}

	st, _ := a.LoadState()
	lc := st.LastClean
	if lc == nil || lc.ID != cleanIDA || lc.ImagesRemoved != 3 || lc.Error != "" || lc.StartedAt == "" || lc.FinishedAt == "" || lc.BytesFreed != 300000000 || lc.VolumesRemoved != 0 {
		t.Errorf("last clean = %+v", lc)
	}
	if out := a.stdout.String(); !strings.Contains(out, "Cleaned up: removed 3 images, freed 300.0 MB") {
		t.Errorf("stdout = %q", out)
	}

	// The next sync, with the same answer, removes nothing, and reports the outcome.
	d.mu.Lock()
	d.calls = nil
	d.mu.Unlock()
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := d.changing(); len(got) != 0 {
		t.Errorf("the same request ran again: %v", got)
	}
	reqs := c.calls("/v1/node/sync")
	space, _ := reqs[len(reqs)-1].body["space"].(map[string]any)
	last, _ := space["lastClean"].(map[string]any)
	if last["id"] != cleanIDA || last["imagesRemoved"].(float64) != 3 || last["bytesFreed"].(float64) != 300000000 {
		t.Errorf("the report's lastClean = %v", last)
	}

	// A new ID runs again.
	c.on("/v1/node/sync", a.cleanAnswer(strings.Repeat("b", 32), pg.Digests, false))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(d.changing()) == 0 {
		t.Error("a new clean-up didn't run")
	}
}

func TestCleanUpWithDockerVolumes(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	d.df = []string{fixture(t, "system-df.txt"), dfAfter(t)} // before, after
	withDocker(a, d)
	st, _ := a.LoadState()
	if err := a.applyClean(ctx(), st, &CleanRequest{ID: cleanIDA, Keep: nil, DockerVolumes: true}); err != nil {
		t.Fatal(err)
	}
	got := d.changing()
	if len(got) < 2 || got[len(got)-2] != "builder prune -af" || got[len(got)-1] != "volume prune -f" {
		t.Errorf("calls = %v", got)
	}
	st, _ = a.LoadState()
	if st.LastClean.VolumesRemoved != 154 || st.LastClean.BytesFreed != 300000000+91050000 {
		t.Errorf("last clean = %+v", st.LastClean)
	}
}

// One image Docker refuses doesn't stop the others; the first failure is kept, cut and cleaned.
func TestCleanUpKeepsTheFirstErrorAndGoesOn(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	d.df = []string{fixture(t, "system-df.txt"), fixture(t, "system-df.txt")} // nothing freed
	d.failOn = "node:24-alpine"
	d.rmErr = errors.New("docker image failed: exit status 1: Error response from daemon: conflict: unable to delete a34ab6d7 (cannot be forced)\n\t" + strings.Repeat("x", 400))
	withDocker(a, d)
	st, _ := a.LoadState()
	if err := a.applyClean(ctx(), st, &CleanRequest{ID: cleanIDA}); err != nil {
		t.Fatal(err)
	}
	st, _ = a.LoadState()
	lc := st.LastClean
	if lc.ImagesRemoved != 3 || lc.FinishedAt == "" {
		t.Errorf("last clean = %+v: three of the four unused images went", lc)
	}
	if lc.Error == "" || len(lc.Error) > maxCleanError || strings.ContainsAny(lc.Error, "\n\t") || !strings.Contains(lc.Error, "conflict") {
		t.Errorf("error = %q", lc.Error)
	}
	if lc.BytesFreed != 0 {
		t.Errorf("freed %d with equal sizes", lc.BytesFreed)
	}
	if got := d.changing(); got[len(got)-1] != "builder prune -af" {
		t.Errorf("the build cache wasn't pruned after the failure: %v", got)
	}
	if out := a.stdout.String(); !strings.Contains(out, "Clean-up failed: ") {
		t.Errorf("stdout = %q", out)
	}
}

// Removing an image's first name can remove the image and its other names: the command then fails on a later
// name ("No such image"), but the image is gone, so it is no failure (install-test.sh, Docker 29).
func TestCleanUpCountsAnImageThatTheFirstNameTookAway(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	d.df = []string{fixture(t, "system-df.txt"), dfAfter(t)}
	d.vanishOn = "node:24-alpine"
	d.vanishErr = errors.New("docker image failed: exit status 1: Error response from daemon: No such image: node@sha256:" + strings.Repeat("a", 64))
	withDocker(a, d)
	st, _ := a.LoadState()
	if err := a.applyClean(ctx(), st, &CleanRequest{ID: cleanIDA}); err != nil {
		t.Fatal(err)
	}
	st, _ = a.LoadState()
	if lc := st.LastClean; lc.Error != "" || lc.ImagesRemoved != 4 {
		t.Errorf("last clean = %+v, want the 4 unused images removed and no error", lc)
	}
}

func TestCleanUpWhenDockerCannotBeRead(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	a.runner.docker = func(args []string) ([]byte, error) {
		return nil, errors.New("docker system failed: Cannot connect to the Docker daemon")
	}
	st, _ := a.LoadState()
	if err := a.applyClean(ctx(), st, &CleanRequest{ID: cleanIDA}); err != nil {
		t.Fatal(err)
	}
	st, _ = a.LoadState()
	if st.LastClean == nil || st.LastClean.Error == "" || st.LastClean.ImagesRemoved != 0 || st.LastClean.FinishedAt == "" {
		t.Errorf("last clean = %+v", st.LastClean)
	}
	for _, c := range a.runner.calls {
		if c.name == "docker" && (firstWord(c.args) == "image" && len(c.args) > 1 && c.args[1] == "rm") {
			t.Errorf("an image was removed: %v", c)
		}
	}
}

// A run that began and never ended (the agent was stopped in the middle) is reported as it is, and the
// same request isn't run again after the restart.
func TestInterruptedCleanUpIsReportedAndNotRepeated(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	withDocker(a, d)
	st, _ := a.LoadState()
	st.LastClean = &CleanOutcome{ID: cleanIDA, StartedAt: "2026-11-20T04:00:00Z"} // what the crash left
	if err := a.SaveState(st); err != nil {
		t.Fatal(err)
	}
	c.on("/v1/node/sync", a.cleanAnswer(cleanIDA, nil, true))
	if err := a.Sync(ctx(), SyncOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := d.changing(); len(got) != 0 {
		t.Errorf("an interrupted clean-up ran again: %v", got)
	}
	space, _ := c.calls("/v1/node/sync")[0].body["space"].(map[string]any)
	last, _ := space["lastClean"].(map[string]any)
	if last["id"] != cleanIDA || last["startedAt"] != "2026-11-20T04:00:00Z" || last["finishedAt"] != "" {
		t.Errorf("lastClean = %v", last)
	}
}

// A state.json that can't be written means nothing is removed.
func TestCleanUpNeedsTheStateWritten(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	withDocker(a, d)
	st, _ := a.LoadState()
	// A file where the agent's folder should be.
	if err := os.RemoveAll(a.file("etc/caravana-node")); err != nil {
		t.Fatal(err)
	}
	a.writeFiles(t, map[string]string{"etc/caravana-node": "in the way"})
	if err := a.applyClean(ctx(), st, &CleanRequest{ID: cleanIDA}); err == nil {
		t.Fatal("a clean-up ran with no way to record it")
	}
	if got := d.changing(); len(got) != 0 {
		t.Errorf("docker changed things: %v", got)
	}
}

// A renewal that fails doesn't keep the clean-up from running, and is the error the sync returns.
func TestCleanUpRunsAfterAFailedRenewal(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	d := newFakeDocker(t)
	withDocker(a, d)
	c.on("/v1/node/certificate", func(map[string]any, string) (int, any) { return 500, nil })
	c.on("/v1/node/sync", a.syncAnswer(func(m map[string]any) {
		m["renew"] = true
		m["clean"] = map[string]any{"id": cleanIDA, "keep": []string{}, "dockerVolumes": false}
	}))
	err := a.Sync(ctx(), SyncOptions{})
	if err != nil {
		t.Fatalf("a soft renewal failure returned %v", err)
	}
	if len(d.changing()) == 0 {
		t.Error("the clean-up didn't run after a renewal that failed")
	}
	if !strings.Contains(a.stderr.String(), "certificate") {
		t.Errorf("stderr = %q", a.stderr.String())
	}
}

func TestCleanTextIsCutAndPlain(t *testing.T) {
	got := shortError("a\x00b\nc\t" + strings.Repeat("y", 500))
	if len(got) != maxCleanError || strings.ContainsAny(got, "\x00\n\t") {
		t.Errorf("shortError = %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for in, want := range map[int64]string{0: "0 B", 999: "999 B", 1500: "1.5 kB", 300000000: "300.0 MB", 1200000000: "1.2 GB"} {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSpaceNowChangesNothing(t *testing.T) {
	c := newConsole(t)
	a := newTestAgent(t, c)
	a.joined(t)
	withDocker(a, newFakeDocker(t))
	if err := os.MkdirAll(a.file("opt/nomad/data/host_volumes/aaaa"), 0o755); err != nil {
		t.Fatal(err)
	}
	a.runner.du = func(args []string) ([]byte, error) { return []byte("8\t" + args[len(args)-1] + "\n"), nil }
	before := a.snapshot(t)
	r, err := a.SpaceNow(ctx())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Images) != 7 || len(r.Volumes) != 1 || r.Volumes[0].Bytes != 8*1024 {
		t.Errorf("space = %+v", r)
	}
	if !sameSnapshot(before, a.snapshot(t)) {
		t.Error("`node space` wrote a file")
	}
	a.runner.docker = func(args []string) ([]byte, error) { return nil, errors.New("down") }
	if _, err := a.SpaceNow(ctx()); err == nil {
		t.Error("an inventory without Docker was printed")
	}
}
