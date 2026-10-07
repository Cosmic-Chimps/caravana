package deploy

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// An app's volumes are Nomad dynamic host volumes (research R5): directories
// on a node that Nomad's built-in "mkdir" plugin creates, named
// nomadjob.VolumeName. The job asks for them by name; Nomad places the copy
// on the node that has them.

// volumeMode is the directory's mode. mkdir makes a root-owned 0700
// directory, which an image whose process isn't root couldn't write to.
// Docker's named volumes take the image's ownership instead; a host volume
// can't, so it is open to every user of the container.
const volumeMode = "0777"

// EnsureVolumes creates every volume of a that Nomad doesn't have yet, and
// waits until all of them are ready. A volume that exists is used as it is,
// with its data. Register calls it, so the first deploy that names a volume
// creates it.
func (d *Deployer) EnsureVolumes(ctx context.Context, a app.App) error {
	var want []string
	for _, svc := range a.Services {
		for _, v := range svc.Volumes {
			want = append(want, nomadjob.VolumeName(a.ID, v.Name))
		}
	}
	if len(want) == 0 {
		return nil
	}
	have, err := d.listVolumes(ctx, nomadjob.VolumePrefix(a.ID))
	if err != nil {
		return err
	}

	ids := make([]string, 0, len(want))
	for _, name := range want {
		if stub, ok := have[name]; ok {
			ids = append(ids, stub.ID)
			continue
		}
		resp, _, err := d.nomad.HostVolumes().Create(&api.HostVolumeCreateRequest{Volume: &api.HostVolume{
			Namespace:  "default",
			Name:       name,
			PluginID:   "mkdir",
			Parameters: map[string]string{"mode": volumeMode},
			RequestedCapabilities: []*api.HostVolumeCapability{{
				AccessMode:     api.HostVolumeAccessModeSingleNodeSingleWriter,
				AttachmentMode: api.HostVolumeAttachmentModeFilesystem,
			}},
		}}, (&api.WriteOptions{}).WithContext(ctx))
		if err != nil {
			return d.volumeError("create volume "+name, err)
		}
		if resp == nil || resp.Volume == nil {
			return fmt.Errorf("%w: Nomad answered no volume for %s", ErrNomadRejected, name)
		}
		ids = append(ids, resp.Volume.ID)
	}

	for i, id := range ids {
		if err := d.waitReady(ctx, id, want[i]); err != nil {
			return err
		}
	}
	return nil
}

// waitReady waits until the volume id is ready. Creating one takes a few
// seconds; the deploy's budget bounds the wait.
func (d *Deployer) waitReady(ctx context.Context, id, name string) error {
	gap := d.Timing.withDefaults().PollGap
	for {
		v, _, err := d.nomad.HostVolumes().Get(id, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return d.volumeError("read volume "+name, err)
		}
		switch v.State {
		case api.HostVolumeStateReady:
			return nil
		case api.HostVolumeStatePending:
		default:
			return fmt.Errorf("%w: volume %s is %s", ErrNomadRejected, name, v.State)
		}
		if !sleep(ctx, gap) {
			return fmt.Errorf("%w: volume %s still isn't ready: %v", ErrNomadUnavailable, name, ctx.Err())
		}
	}
}

// DeleteVolumes deletes every volume of appID, and the data in them. Call it
// after Purge: Nomad refuses to delete a volume while a copy uses it, and
// the purged job's copies take a moment to stop, so that refusal is retried
// for up to Timing.VolumeDeleteWait. A volume already gone counts as deleted.
func (d *Deployer) DeleteVolumes(ctx context.Context, appID string) error {
	have, err := d.listVolumes(ctx, nomadjob.VolumePrefix(appID))
	if err != nil {
		return err
	}
	timing := d.Timing.withDefaults()
	for name, stub := range have {
		deadline := time.Now().Add(timing.VolumeDeleteWait)
		gap := timing.BackoffStart
		for {
			_, _, err := d.nomad.HostVolumes().Delete(&api.HostVolumeDeleteRequest{ID: stub.ID}, (&api.WriteOptions{}).WithContext(ctx))
			if err == nil || statusOf(err) == http.StatusNotFound {
				break
			}
			if !inUse(err) || time.Now().After(deadline) {
				return d.volumeError("delete volume "+name, err)
			}
			if !sleep(ctx, gap) {
				return d.volumeError("delete volume "+name, err)
			}
			gap = min(2*gap, timing.BackoffMax)
		}
	}
	return nil
}

// listVolumes returns the host volumes whose names start with prefix, by
// name. The prefix ends in "_", so one app's never match another's.
func (d *Deployer) listVolumes(ctx context.Context, prefix string) (map[string]*api.HostVolumeStub, error) {
	stubs, _, err := d.nomad.HostVolumes().List(&api.HostVolumeListRequest{}, (&api.QueryOptions{Prefix: prefix}).WithContext(ctx))
	if err != nil {
		return nil, d.volumeError("list volumes "+prefix+"*", err)
	}
	out := make(map[string]*api.HostVolumeStub, len(stubs))
	for _, s := range stubs {
		if strings.HasPrefix(s.Name, prefix) {
			out[s.Name] = s
		}
	}
	return out, nil
}

// inUse reports whether Nomad refused a delete because a copy still uses
// the volume.
func inUse(err error) bool {
	var ure api.UnexpectedResponseError
	return errors.As(err, &ure) && strings.Contains(ure.Body(), "in use by allocations")
}

// volumeError is callError for volume calls, except that Nomad refusing the
// request for another reason than the token is ErrNomadRejected with what
// it said.
func (d *Deployer) volumeError(action string, err error) error {
	var ure api.UnexpectedResponseError
	if errors.As(err, &ure) {
		if ure.StatusCode() == http.StatusForbidden {
			return fmt.Errorf("%w: Nomad refused the token (%s); check NOMAD_TOKEN and that its policy allows host volumes", ErrNomadUnavailable, action)
		}
		return fmt.Errorf("%w: %s: Nomad answered %d: %s", ErrNomadRejected, action, ure.StatusCode(), strings.TrimSpace(ure.Body()))
	}
	return d.callError(action, err)
}
