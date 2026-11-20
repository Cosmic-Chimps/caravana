package nodeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Reclaiming space (feature 045, contracts/agent.md). The agent tells the console what Docker holds
// (images, Docker's leftover volumes, the build cache) and how big each Nomad host volume directory is,
// and it runs the clean-up the console hands it. The console sends an ID and the digests to KEEP, never
// a path, a container or an image to remove: the agent removes what no container uses and isn't kept.
// It talks to Docker with the Runner, the same way it asks `docker info`, and Docker refuses to remove
// an image a container uses, which is the second guard after the agent's own check.

// The limits and timings of the space block.
const (
	// maxSpaceImages is the most images a report names; past it the largest are kept and
	// imagesTruncated says so (contracts/agent.md §1).
	maxSpaceImages = 1000
	// maxReportRefs is the most digests and tags of one image a report names; the console refuses more.
	maxReportRefs = 16
	// maxSpaceJSON is the most the space block may weigh as JSON: past it the smallest images are left out
	// (and imagesTruncated says so), so a report always fits what the console accepts of a sync.
	maxSpaceJSON = 2 << 20
	// maxRefLen is the longest tag or image ID a report names.
	maxRefLen = 300
	// maxSpaceBytes is the biggest size a report carries (2^50), where the console draws the line.
	maxSpaceBytes = int64(1) << 50
	// volumeMeasureEvery is how old the sizes of the volume directories get before they are measured again.
	volumeMeasureEvery = 15 * time.Minute
	// measureBudget is how long one round of measuring may take, whatever the number of volumes.
	measureBudget = 3 * time.Minute
	// duTimeout is how long `du` may take for one volume directory.
	duTimeout = 2 * time.Minute
	// cleanBudget is how long a whole clean-up may take (contracts/agent.md §3).
	cleanBudget = 10 * time.Minute
	// cleanCommandTimeout is how long one Docker command of a clean-up may take: a build cache or a big
	// image takes longer than the 30 seconds an ordinary call of the agent gets.
	cleanCommandTimeout = 5 * time.Minute
	// inspectBatch is how many IDs one `docker inspect` is given.
	inspectBatch = 100
	// maxCleanError is the longest error a clean-up records.
	maxCleanError = 200

	// hostVolumesDir is where Nomad's mkdir plugin makes a dynamic host volume's directory, named by the
	// volume's ID (research R3).
	hostVolumesDir = nomadDataDir + "/host_volumes"
)

var (
	imageIDRE  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	digestRE   = imageIDRE // a digest has the same shape as an image ID
	volumeIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	cleanIDRE  = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// containerIDRE is what `docker container ls -q` prints: the short or the whole ID.
	containerIDRE = regexp.MustCompile(`^[0-9a-f]{12,64}$`)
)

// SpaceImage is one image Docker holds, as the report names it.
type SpaceImage struct {
	ID      string   `json:"id"`
	Digests []string `json:"digests"` // the sha256:… parts of RepoDigests
	Tags    []string `json:"tags"`    // RepoTags without <none>:<none>
	Bytes   int64    `json:"bytes"`
	InUse   bool     `json:"inUse"` // a container, running or stopped, uses it

	// refs are the names `docker image rm` is given (every tag and every repo@digest): an image known
	// by two names is untagged twice and the last untag deletes it, where its ID alone would be refused.
	// Never sent.
	refs []string
}

// DockerVolumes is what Docker's local volumes that no container uses hold.
type DockerVolumes struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// SpaceVolume is one Nomad host volume directory and its size, by the directory's name (a Nomad ID).
type SpaceVolume struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

// CleanOutcome is what the last clean-up this agent ran did. A FinishedAt that is empty is a run that
// began and never ended: the agent was stopped in the middle, which the console reads as interrupted.
type CleanOutcome struct {
	ID             string `json:"id"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt"`
	ImagesRemoved  int    `json:"imagesRemoved"`
	BytesFreed     int64  `json:"bytesFreed"`
	VolumesRemoved int    `json:"volumesRemoved"`
	Error          string `json:"error"`
}

// SpaceReport is the report's space block (contracts/agent.md §1).
type SpaceReport struct {
	Images            []SpaceImage  `json:"images"`
	ImagesTruncated   bool          `json:"imagesTruncated"`
	DockerVolumes     DockerVolumes `json:"dockerVolumes"`
	BuildCacheBytes   int64         `json:"buildCacheBytes"`
	Volumes           []SpaceVolume `json:"volumes"`
	VolumesMeasuredAt string        `json:"volumesMeasuredAt,omitempty"`
	LastClean         *CleanOutcome `json:"lastClean,omitempty"`
}

// CleanRequest is the console's answer's clean block: the ID of the clean-up, the digests of the images
// to keep, and whether Docker's leftover volumes go too.
type CleanRequest struct {
	ID            string   `json:"id"`
	Keep          []string `json:"keep"`
	DockerVolumes bool     `json:"dockerVolumes"`
}

// longRunner is a Runner that can give a longer time to one program. ExecRunner is one; a test's fake
// need not be.
type longRunner interface {
	WithTimeout(time.Duration) Runner
}

// runLong runs a program that may take longer than an ordinary call (du, a clean-up's Docker commands).
func (a *Agent) runLong(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	r := a.Runner
	if lr, ok := r.(longRunner); ok {
		r = lr.WithTimeout(timeout)
	}
	return r.Run(ctx, name, args...)
}

// ---- the inventory ----------------------------------------------------------------------------

// inspectedImage is the part of `docker image inspect` the agent reads.
type inspectedImage struct {
	ID          string   `json:"Id"`
	RepoDigests []string `json:"RepoDigests"`
	RepoTags    []string `json:"RepoTags"`
	Size        int64    `json:"Size"`
}

// inventory lists every image Docker holds with what uses it (research R4): the image IDs, one inspect
// each for names and size, and the image each container, running or stopped, uses. A Docker that
// doesn't answer is an error, and so is a container list the agent can't read in full: with it the
// agent would call an image unused that a container uses.
func (a *Agent) inventory(ctx context.Context) ([]SpaceImage, error) {
	out, err := a.run(ctx, "docker", "image", "ls", "-q", "--no-trunc")
	if err != nil {
		return nil, fmt.Errorf("list the images: %w", err)
	}
	ids := uniqueLines(out, imageIDRE)

	var images []SpaceImage
	for _, batch := range batches(ids, inspectBatch) {
		args := append([]string{"image", "inspect", "--format", "{{json .}}"}, batch...)
		out, err := a.run(ctx, "docker", args...)
		parsed := parseImageInspect(out)
		// An image removed between the list and the inspect fails its line only: the rest is read.
		if err != nil && len(parsed) == 0 {
			return nil, fmt.Errorf("inspect the images: %w", err)
		}
		images = append(images, parsed...)
	}

	used, err := a.imagesInUse(ctx)
	if err != nil {
		return nil, err
	}
	for i := range images {
		images[i].InUse = used[images[i].ID]
	}
	return images, nil
}

// imagesInUse returns the IDs of the images the containers, running or stopped, use.
func (a *Agent) imagesInUse(ctx context.Context) (map[string]bool, error) {
	out, err := a.run(ctx, "docker", "container", "ls", "-a", "-q")
	if err != nil {
		return nil, fmt.Errorf("list the containers: %w", err)
	}
	used := map[string]bool{}
	for _, batch := range batches(uniqueLines(out, containerIDRE), inspectBatch) {
		args := append([]string{"container", "inspect", "--format", "{{.Image}}"}, batch...)
		out, err := a.run(ctx, "docker", args...)
		if err != nil {
			return nil, fmt.Errorf("inspect the containers: %w", err)
		}
		for _, id := range uniqueLines(out, imageIDRE) {
			used[id] = true
		}
	}
	return used, nil
}

// parseImageInspect reads `docker image inspect --format '{{json .}}'`: one JSON object a line. A line
// that isn't one is skipped.
func parseImageInspect(out []byte) []SpaceImage {
	var images []SpaceImage
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var in inspectedImage
		if json.Unmarshal([]byte(line), &in) != nil || !imageIDRE.MatchString(in.ID) {
			continue
		}
		img := SpaceImage{ID: in.ID, Bytes: in.Size, Digests: []string{}, Tags: []string{}}
		seen := map[string]bool{}
		for _, ref := range in.RepoDigests {
			if _, digest, ok := strings.Cut(ref, "@"); ok && digestRE.MatchString(digest) && !seen[digest] {
				seen[digest] = true
				img.Digests = append(img.Digests, digest)
			}
		}
		for _, tag := range in.RepoTags {
			if tag != "" && tag != "<none>:<none>" {
				img.Tags = append(img.Tags, tag)
			}
		}
		img.refs = refsOf(in)
		images = append(images, img)
	}
	return images
}

// refsOf are the names an image is removed by: every tag and every repo@digest, each once. A name that
// could be read as an option is left out.
func refsOf(in inspectedImage) []string {
	var refs []string
	seen := map[string]bool{}
	for _, ref := range append(append([]string(nil), in.RepoTags...), in.RepoDigests...) {
		if strings.HasSuffix(ref, "@") || (slices.Contains(in.RepoDigests, ref) && !strings.Contains(ref, "@sha256:")) {
			continue
		}
		if ref == "" || ref == "<none>:<none>" || ref == "<none>@<none>" || strings.HasPrefix(ref, "-") || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	return refs
}

// uniqueLines returns the distinct lines of out that match re, in order.
func uniqueLines(out []byte, re *regexp.Regexp) []string {
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && re.MatchString(line) && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	return lines
}

// batches cuts a list into pieces of at most n.
func batches(list []string, n int) [][]string {
	var out [][]string
	for len(list) > n {
		out = append(out, list[:n])
		list = list[n:]
	}
	if len(list) > 0 {
		out = append(out, list)
	}
	return out
}

// ---- docker system df -------------------------------------------------------------------------

// dfLine is one line of `docker system df --format '{{json .}}'`, with Docker's human sizes read as bytes.
type dfLine struct {
	Total, Active, Size, Reclaimable int64
}

// dockerDF is `docker system df` by type: "Images", "Containers", "Local Volumes", "Build Cache".
type dockerDF map[string]dfLine

// flexString reads a JSON string or number as text: Docker's template prints strings, a newer one may
// print numbers.
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flexString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = flexString(n.String())
	return nil
}

// parseDockerDF reads `docker system df --format '{{json .}}'` (research R4). Output with no line of
// that shape is an error: the Docker that answered isn't one the agent can read.
func parseDockerDF(out string) (dockerDF, error) {
	df := dockerDF{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw struct {
			Type        string     `json:"Type"`
			TotalCount  flexString `json:"TotalCount"`
			Active      flexString `json:"Active"`
			Size        flexString `json:"Size"`
			Reclaimable flexString `json:"Reclaimable"`
		}
		if json.Unmarshal([]byte(line), &raw) != nil || raw.Type == "" {
			continue
		}
		total, err1 := strconv.ParseInt(strings.TrimSpace(string(raw.TotalCount)), 10, 64)
		active, err2 := strconv.ParseInt(strings.TrimSpace(string(raw.Active)), 10, 64)
		size, err3 := parseHumanSize(string(raw.Size))
		// Reclaimable reads "326.5MB (22%)"; the share isn't used.
		recl, _, _ := strings.Cut(string(raw.Reclaimable), " ")
		reclaimable, err4 := parseHumanSize(recl)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return nil, fmt.Errorf("docker system df: a %q line isn't one the agent reads", raw.Type)
		}
		df[raw.Type] = dfLine{Total: total, Active: active, Size: size, Reclaimable: reclaimable}
	}
	if len(df) == 0 {
		return nil, errors.New("docker system df: no line the agent reads")
	}
	return df, nil
}

// humanUnits are the multipliers of Docker's sizes: decimal, as Docker's own formatting writes them.
var humanUnits = map[string]float64{
	"b": 1, "kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12, "pb": 1e15,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// parseHumanSize reads a size as Docker prints it ("0B", "8.192kB", "326.5MB", "1.472GB") in bytes,
// rounded. Docker prints four significant digits, so a large size is only that exact.
func parseHumanSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("%q isn't a size", s)
	}
	n, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, fmt.Errorf("%q isn't a size", s)
	}
	mult, ok := humanUnits[strings.ToLower(strings.TrimSpace(s[i:]))]
	if !ok {
		return 0, fmt.Errorf("%q isn't a size", s)
	}
	b := math.Round(n * mult)
	if b < 0 || b > float64(maxSpaceBytes) {
		return 0, fmt.Errorf("%q is out of range", s)
	}
	return int64(b), nil
}

// dockerDF runs `docker system df` and reads it.
func (a *Agent) dockerDF(ctx context.Context) (dockerDF, error) {
	out, err := a.run(ctx, "docker", "system", "df", "--format", "{{json .}}")
	if err != nil {
		return nil, err
	}
	return parseDockerDF(string(out))
}

// ---- the report's space block -----------------------------------------------------------------

// space gathers the report's space block. A Docker that doesn't answer leaves it nil: the console then
// says there is no inventory yet. The volume sizes come from st, measured by measureVolumes.
func (a *Agent) space(ctx context.Context, st State) *SpaceReport {
	images, err := a.inventory(ctx)
	if err != nil {
		return nil
	}
	df, err := a.dockerDF(ctx)
	if err != nil {
		return nil
	}
	r := &SpaceReport{
		Images:            reportImages(images),
		DockerVolumes:     DockerVolumes{Bytes: df["Local Volumes"].Reclaimable},
		BuildCacheBytes:   df["Build Cache"].Reclaimable,
		Volumes:           reportVolumes(st.VolumeSizes),
		VolumesMeasuredAt: st.VolumesMeasuredAt,
		LastClean:         st.LastClean,
	}
	r.ImagesTruncated = len(images) > maxSpaceImages
	if r.ImagesTruncated {
		r.Images = r.Images[:maxSpaceImages]
	}
	// Images are largest first, so halving the list drops the smallest.
	for len(r.Images) > 1 {
		b, err := json.Marshal(r)
		if err != nil || len(b) <= maxSpaceJSON {
			break
		}
		r.Images, r.ImagesTruncated = r.Images[:len(r.Images)/2], true
	}
	if v := df["Local Volumes"]; v.Total > v.Active {
		r.DockerVolumes.Count = v.Total - v.Active
	}
	return r
}

// reportImages turns the inventory into what the console accepts: the largest first, the digests and
// tags capped, and the text it would refuse left out.
func reportImages(images []SpaceImage) []SpaceImage {
	out := make([]SpaceImage, 0, len(images))
	for _, img := range images {
		img.Bytes = min(max(img.Bytes, 0), maxSpaceBytes)
		img.Digests = capped(img.Digests)
		img.Tags = capped(printableTags(img.Tags))
		out = append(out, img)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func capped(list []string) []string {
	if len(list) > maxReportRefs {
		list = list[:maxReportRefs]
	}
	if list == nil {
		list = []string{}
	}
	return list
}

// printableTags drops the tags the console would refuse: too long, or with a control character.
func printableTags(tags []string) []string {
	var out []string
	for _, t := range tags {
		if len(t) > maxRefLen || strings.ContainsFunc(t, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// reportVolumes lists the measured volume directories by name; a name the console wouldn't accept is
// left out.
func reportVolumes(sizes map[string]int64) []SpaceVolume {
	out := make([]SpaceVolume, 0, len(sizes))
	for id, bytes := range sizes {
		if volumeIDRE.MatchString(id) && bytes >= 0 {
			out = append(out, SpaceVolume{ID: id, Bytes: min(bytes, maxSpaceBytes)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- the volume directories -------------------------------------------------------------------

// measureVolumes measures the Nomad host volume directories with `du -sk` when the sizes in st are
// older than volumeMeasureEvery (or force says so), and returns the new state and whether it changed.
// It writes nothing: the caller saves the state. A host with no host_volumes folder has no volumes to
// measure and keeps what it has. A directory `du` can't read keeps its last size; the whole round has
// measureBudget. The directory names are checked against volumeIDRE, so a name never becomes a path
// of something else.
func (a *Agent) measureVolumes(ctx context.Context, st State, force bool) (State, bool) {
	dir := a.path(hostVolumesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return st, false
	}
	if !force {
		if at, err := time.Parse(time.RFC3339, st.VolumesMeasuredAt); err == nil && a.now().Sub(at) < volumeMeasureEvery {
			return st, false
		}
	}
	ctx, cancel := context.WithTimeout(ctx, measureBudget)
	defer cancel()
	sizes := map[string]int64{}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || !volumeIDRE.MatchString(name) {
			continue
		}
		out, err := a.runLong(ctx, duTimeout, "du", "-sk", filepath.Join(dir, name))
		if kb, ok := parseDU(string(out)); err == nil && ok {
			sizes[name] = kb * 1024
		} else if old, ok := st.VolumeSizes[name]; ok {
			sizes[name] = old
		}
	}
	st.VolumeSizes = sizes
	st.VolumesMeasuredAt = a.now().UTC().Format(time.RFC3339)
	return st, true
}

// parseDU reads `du -sk`'s line, "<kilobytes>\t<path>", as the kilobytes.
func parseDU(out string) (kb int64, ok bool) {
	f := strings.Fields(out)
	if len(f) == 0 {
		return 0, false
	}
	kb, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil || kb < 0 || kb > maxSpaceBytes/1024 {
		return 0, false
	}
	return kb, true
}

// SpaceNow is what `caravana node space` prints: the space block as the next sync would send it, with
// the volume directories measured now. It changes nothing on the host, state.json included.
func (a *Agent) SpaceNow(ctx context.Context) (*SpaceReport, error) {
	st, err := a.LoadState()
	if err != nil {
		return nil, err
	}
	st, _ = a.measureVolumes(ctx, st, true)
	r := a.space(ctx, st)
	if r == nil {
		return nil, errors.New("docker doesn't answer, so there is no inventory")
	}
	return r, nil
}

// ---- the clean-up -----------------------------------------------------------------------------

// removable is the plan of a clean-up: the images that no container uses and none of whose digests the
// console asked to keep. An image with no digest at all (one built on the host) is not kept by the
// list, only by a container using it.
func removable(images []SpaceImage, keep []string) []SpaceImage {
	kept := make(map[string]bool, len(keep))
	for _, d := range keep {
		kept[d] = true
	}
	var out []SpaceImage
	for _, img := range images {
		if img.InUse {
			continue
		}
		isKept := false
		for _, d := range img.Digests {
			if kept[d] {
				isKept = true
				break
			}
		}
		if !isKept {
			out = append(out, img)
		}
	}
	return out
}

// cleanResult is what a clean-up did, before it is dressed as a CleanOutcome.
type cleanResult struct {
	imagesRemoved  int
	bytesFreed     int64
	volumesRemoved int
	err            string // the first failure, cleaned and cut
}

// fail keeps the first failure only: the console shows one reason.
func (r *cleanResult) fail(format string, args ...any) {
	if r.err == "" {
		r.err = shortError(fmt.Sprintf(format, args...))
	}
}

// shortError makes a Docker message safe to keep: no control characters, at most maxCleanError bytes.
// Docker's messages name an image at most, never a secret.
func shortError(s string) string {
	s = clean(s)
	if len(s) > maxCleanError {
		s = s[:maxCleanError]
	}
	return s
}

// clean runs a clean-up (contracts/agent.md §3, steps 2 to 6): the sizes before, every removable image
// removed, the build cache pruned, Docker's leftover volumes pruned when asked, the sizes after. A step
// that fails is recorded and the next one still runs, so one stuck image doesn't stop the rest. (The
// method is not the text cleaner `clean` of agent.go, which it calls.)
func (a *Agent) clean(ctx context.Context, req *CleanRequest) cleanResult {
	var res cleanResult
	before, err := a.dockerDF(ctx)
	if err != nil {
		res.fail("couldn't read Docker's sizes: %v", err)
		return res
	}
	images, err := a.inventory(ctx)
	if err != nil {
		res.fail("couldn't list what Docker holds: %v", err)
		return res
	}
	for _, img := range removable(images, req.Keep) {
		if ctx.Err() != nil {
			res.fail("stopped: the clean-up ran out of time")
			break
		}
		args := []string{"image", "rm"}
		if len(img.refs) > 0 {
			args = append(args, img.refs...)
		} else {
			args = append(args, img.ID)
		}
		if _, err := a.runLong(ctx, cleanCommandTimeout, "docker", args...); err != nil && !a.imageGone(ctx, img.ID) {
			res.fail("%v", err)
			continue
		}
		res.imagesRemoved++
	}
	if _, err := a.runLong(ctx, cleanCommandTimeout, "docker", "builder", "prune", "-af"); err != nil {
		res.fail("%v", err)
	}
	if req.DockerVolumes {
		if _, err := a.runLong(ctx, cleanCommandTimeout, "docker", "volume", "prune", "-f"); err != nil {
			res.fail("%v", err)
		}
	}
	// The sizes after are read even when time ran out above, with a context of their own.
	after, err := a.dockerDF(context.WithoutCancel(ctx))
	if err != nil {
		res.fail("couldn't read Docker's sizes after: %v", err)
		return res
	}
	// Containers are left out (a running one grows by itself) and so are Docker's volumes unless the
	// clean-up was asked to prune them.
	types := []string{"Images", "Build Cache"}
	if req.DockerVolumes {
		types = append(types, "Local Volumes")
	}
	for _, typ := range types {
		res.bytesFreed += before[typ].Size - after[typ].Size
	}
	res.bytesFreed = max(res.bytesFreed, 0)
	if req.DockerVolumes {
		res.volumesRemoved = int(max(before["Local Volumes"].Total-after["Local Volumes"].Total, 0))
	}
	return res
}

// imageGone says Docker no longer has the image. `docker image rm` of an image's several names can fail
// on the last ones ("No such image: repo@sha256:…", seen on Docker 29 in install-test.sh) because removing
// the first name already removed the image and its other names, so a failed command is only a failure when
// the image is still there. Any other answer from Docker, or none, counts as still there.
func (a *Agent) imageGone(ctx context.Context, id string) bool {
	_, err := a.runLong(ctx, cleanCommandTimeout, "docker", "image", "inspect", "--format", "{{.Id}}", id)
	return err != nil && strings.Contains(err.Error(), "No such image")
}

// applyClean is step 4 of a sync: the console's clean-up request, run once per ID. The ID is written to
// state.json first, so a crash in the middle never repeats the clean-up; its outcome then reads as a run
// that began and never ended. The outcome is written when it ends, the volume directories are measured
// again, and one line is printed. An error is returned only when state.json can't be written, in which
// case nothing was removed.
func (a *Agent) applyClean(ctx context.Context, st State, req *CleanRequest) error {
	if req == nil || (st.LastClean != nil && st.LastClean.ID == req.ID) {
		return nil
	}
	started := a.now().UTC()
	st.LastClean = &CleanOutcome{ID: req.ID, StartedAt: started.Format(time.RFC3339)}
	if err := a.SaveState(st); err != nil {
		return fmt.Errorf("record the clean-up: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, cleanBudget)
	res := a.clean(cctx, req)
	cancel()

	st.LastClean = &CleanOutcome{
		ID: req.ID, StartedAt: started.Format(time.RFC3339), FinishedAt: a.now().UTC().Format(time.RFC3339),
		ImagesRemoved: res.imagesRemoved, BytesFreed: res.bytesFreed, VolumesRemoved: res.volumesRemoved, Error: res.err,
	}
	st, _ = a.measureVolumes(ctx, st, true)
	if err := a.SaveState(st); err != nil {
		return fmt.Errorf("record the clean-up's outcome: %w", err)
	}
	if res.err != "" {
		a.out("Clean-up failed: %s", res.err)
	} else {
		a.out("Cleaned up: removed %d images, freed %s", res.imagesRemoved, humanBytes(res.bytesFreed))
	}
	return nil
}

// humanBytes writes a size the way the console does: decimal units, one digit.
func humanBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	case b >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(b)/1e3)
	}
	return fmt.Sprintf("%d B", b)
}
