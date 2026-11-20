package service

import (
	"fmt"
	"regexp"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// What a server's agent tells about the space it holds (feature 045, contracts/agent.md §1): the
// report's `space` block. The agent's own types are in internal/nodeagent; these are the console's
// reading of the same JSON, checked by validateSpace before anything is stored.

// The limits of the space block, from the contract.
const (
	maxSpaceImages  = 1000
	maxSpaceRefs    = 16 // digests, and tags, of one image
	maxSpaceVolumes = 4096
	maxSpaceText    = 300 // an image's ID or tag
	maxSpaceError   = 500
	maxSpaceBytes   = int64(1) << 50
)

var (
	spaceDigestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	spaceVolumeRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	spaceCleanRE  = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// NodeImage is one image a server's Docker holds.
type NodeImage struct {
	ID      string   `json:"id"`
	Digests []string `json:"digests"` // the sha256:… parts of its repo digests
	Tags    []string `json:"tags"`
	Bytes   int64    `json:"bytes"`
	InUse   bool     `json:"inUse"` // a container, running or stopped, uses it
}

// NodeCount is a number of things and the bytes they hold.
type NodeCount struct {
	Count int64 `json:"count"`
	Bytes int64 `json:"bytes"`
}

// NodeVolumeSize is the size of one Nomad host volume directory, by the directory's name (the volume's ID).
type NodeVolumeSize struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

// NodeClean is the outcome of the last clean-up the agent ran. A FinishedAt that is empty is one that began
// and never ended.
type NodeClean struct {
	ID             string `json:"id"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt"`
	ImagesRemoved  int64  `json:"imagesRemoved"`
	BytesFreed     int64  `json:"bytesFreed"`
	VolumesRemoved int64  `json:"volumesRemoved"`
	Error          string `json:"error"`
}

// NodeSpace is the report's space block. Absent for an agent older than the feature, and when Docker
// didn't answer.
type NodeSpace struct {
	Images            []NodeImage      `json:"images"`
	ImagesTruncated   bool             `json:"imagesTruncated"`
	DockerVolumes     NodeCount        `json:"dockerVolumes"`
	BuildCacheBytes   int64            `json:"buildCacheBytes"`
	Volumes           []NodeVolumeSize `json:"volumes"`
	VolumesMeasuredAt string           `json:"volumesMeasuredAt,omitempty"`
	LastClean         *NodeClean       `json:"lastClean,omitempty"`
}

// CleanRequest is the clean block of a sync answer: what the agent is asked to clean up. It names the
// digests to keep and never an image to remove.
type CleanRequest struct {
	ID            string   `json:"id"`
	Keep          []string `json:"keep"`
	DockerVolumes bool     `json:"dockerVolumes"`
}

// validateSpace checks the sizes and shapes of a report's space block. A bad block is a field error
// naming the first thing wrong, like any other field of the report.
func validateSpace(sp *NodeSpace) error {
	if sp == nil {
		return nil
	}
	if len(sp.Images) > maxSpaceImages {
		return app.NewFieldError("space.images", "has more than %d entries", maxSpaceImages)
	}
	for i, img := range sp.Images {
		f := fmt.Sprintf("space.images[%d]", i)
		if img.ID == "" {
			return app.NewFieldError(f+".id", "is empty")
		}
		if err := checkTextMax(f+".id", img.ID, maxSpaceText); err != nil {
			return err
		}
		if len(img.Digests) > maxSpaceRefs {
			return app.NewFieldError(f+".digests", "has more than %d entries", maxSpaceRefs)
		}
		for j, d := range img.Digests {
			if !spaceDigestRE.MatchString(d) {
				return app.NewFieldError(fmt.Sprintf("%s.digests[%d]", f, j), "must be sha256: and 64 lowercase hex characters")
			}
		}
		if len(img.Tags) > maxSpaceRefs {
			return app.NewFieldError(f+".tags", "has more than %d entries", maxSpaceRefs)
		}
		for j, t := range img.Tags {
			if err := checkTextMax(fmt.Sprintf("%s.tags[%d]", f, j), t, maxSpaceText); err != nil {
				return err
			}
		}
		if err := checkBytes(f+".bytes", img.Bytes); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		field string
		value int64
	}{{"space.dockerVolumes.count", sp.DockerVolumes.Count}, {"space.dockerVolumes.bytes", sp.DockerVolumes.Bytes},
		{"space.buildCacheBytes", sp.BuildCacheBytes}} {
		if err := checkBytes(c.field, c.value); err != nil {
			return err
		}
	}
	if len(sp.Volumes) > maxSpaceVolumes {
		return app.NewFieldError("space.volumes", "has more than %d entries", maxSpaceVolumes)
	}
	for i, v := range sp.Volumes {
		f := fmt.Sprintf("space.volumes[%d]", i)
		if !spaceVolumeRE.MatchString(v.ID) {
			return app.NewFieldError(f+".id", "must be 1 to 64 characters of letters, digits and -")
		}
		if err := checkBytes(f+".bytes", v.Bytes); err != nil {
			return err
		}
	}
	if err := checkTimeOrEmpty("space.volumesMeasuredAt", sp.VolumesMeasuredAt); err != nil {
		return err
	}
	return validateNodeClean(sp.LastClean)
}

func validateNodeClean(c *NodeClean) error {
	if c == nil {
		return nil
	}
	if c.ID != "" && !spaceCleanRE.MatchString(c.ID) {
		return app.NewFieldError("space.lastClean.id", "must be 32 lowercase hex characters or empty")
	}
	for _, t := range []struct{ field, value string }{
		{"space.lastClean.startedAt", c.StartedAt}, {"space.lastClean.finishedAt", c.FinishedAt}} {
		if err := checkTimeOrEmpty(t.field, t.value); err != nil {
			return err
		}
	}
	for _, n := range []struct {
		field string
		value int64
	}{{"space.lastClean.imagesRemoved", c.ImagesRemoved}, {"space.lastClean.bytesFreed", c.BytesFreed},
		{"space.lastClean.volumesRemoved", c.VolumesRemoved}} {
		if err := checkBytes(n.field, n.value); err != nil {
			return err
		}
	}
	return checkTextMax("space.lastClean.error", c.Error, maxSpaceError)
}

// checkBytes refuses a count or size outside 0 to 2^50.
func checkBytes(field string, v int64) error {
	if v < 0 || v > maxSpaceBytes {
		return app.NewFieldError(field, "is out of range")
	}
	return nil
}

// checkTimeOrEmpty accepts an RFC 3339 time or an empty text.
func checkTimeOrEmpty(field, v string) error {
	if v == "" {
		return nil
	}
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		return app.NewFieldError(field, "must be an RFC 3339 time or empty")
	}
	return nil
}

// checkTextMax is checkText with its own limit.
func checkTextMax(field, v string, limit int) error {
	if len(v) > limit {
		return app.NewFieldError(field, "is longer than %d characters", limit)
	}
	for _, r := range v {
		if r < ' ' || r == 0x7f {
			return app.NewFieldError(field, "has a control character")
		}
	}
	return nil
}
