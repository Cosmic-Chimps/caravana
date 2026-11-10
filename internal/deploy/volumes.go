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
// with its data, and is never moved. Register calls it, so the first deploy
// that names a volume creates it.
//
// All the volumes of one service live on one server, or the service could
// never be placed (feature 031, research L12 and D9). So per service: when one
// of its volumes exists, its node decides and the volumes still to create go
// on that node; otherwise the first one is created with no node (Nomad picks
// one in the "default" pool), and the others follow it to the node Nomad
// chose. Two services may end up on different servers.
func (d *Deployer) EnsureVolumes(ctx context.Context, a app.App) error {
	haveAny := false
	for _, svc := range a.Services {
		if len(svc.Volumes) > 0 {
			haveAny = true
		}
	}
	if !haveAny {
		return nil
	}
	have, err := d.listVolumes(ctx, nomadjob.VolumePrefix(a.ID))
	if err != nil {
		return err
	}

	// Decide every service's node before creating anything, so a service
	// whose volumes are already split refuses the deploy with nothing changed.
	nodes := make([]string, len(a.Services))
	for i, svc := range a.Services {
		node, err := existingNode(a.ID, svc, have)
		if err != nil {
			return err
		}
		nodes[i] = node
	}

	var ids, names []string
	for i, svc := range a.Services {
		node := nodes[i]
		for _, v := range svc.Volumes {
			name := nomadjob.VolumeName(a.ID, v.Name)
			names = append(names, name)
			if stub, ok := have[name]; ok {
				ids = append(ids, stub.ID)
				continue
			}
			vol, err := d.createVolume(ctx, name, node)
			if err != nil {
				return err
			}
			ids = append(ids, vol.ID)
			if node == "" {
				// The first volume of the service: the rest go where it went.
				if vol.NodeID == "" {
					return fmt.Errorf("%w: Nomad answered no node for volume %s", ErrNomadRejected, name)
				}
				node = vol.NodeID
			}
		}
	}

	for i, id := range ids {
		if err := d.waitReady(ctx, id, names[i]); err != nil {
			return err
		}
	}
	return nil
}

// existingNode returns the node of svc's volumes that Nomad already has, or
// "" when it has none. Two of them on different nodes is an error naming the
// service and both volumes: nothing can be done about it by creating more.
func existingNode(appID string, svc app.Service, have map[string]*api.HostVolumeStub) (string, error) {
	node, first := "", ""
	for _, v := range svc.Volumes {
		name := nomadjob.VolumeName(appID, v.Name)
		stub, ok := have[name]
		if !ok || stub.NodeID == "" {
			continue
		}
		switch {
		case node == "":
			node, first = stub.NodeID, name
		case stub.NodeID != node:
			return "", fmt.Errorf("%w: service %s has its volumes on different servers (%s on node %s, %s on node %s); a service's volumes must be on one server",
				ErrNomadRejected, svc.Name, first, node, name, stub.NodeID)
		}
	}
	return node, nil
}

// createVolume creates one dynamic host volume and returns what Nomad
// answered. With a node the volume goes there; without one Nomad picks a node
// of the "default" pool.
func (d *Deployer) createVolume(ctx context.Context, name, nodeID string) (*api.HostVolume, error) {
	vol := &api.HostVolume{
		Namespace:  "default",
		Name:       name,
		PluginID:   "mkdir",
		Parameters: map[string]string{"mode": volumeMode},
		RequestedCapabilities: []*api.HostVolumeCapability{{
			AccessMode:     api.HostVolumeAccessModeSingleNodeSingleWriter,
			AttachmentMode: api.HostVolumeAttachmentModeFilesystem,
		}},
	}
	if nodeID != "" {
		vol.NodeID = nodeID
	} else {
		vol.NodePool = "default"
	}
	resp, _, err := d.nomad.HostVolumes().Create(&api.HostVolumeCreateRequest{Volume: vol}, (&api.WriteOptions{}).WithContext(ctx))
	if err != nil {
		return nil, d.volumeError("create volume "+name, err)
	}
	if resp == nil || resp.Volume == nil {
		return nil, fmt.Errorf("%w: Nomad answered no volume for %s", ErrNomadRejected, name)
	}
	return resp.Volume, nil
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

// HostVolumeInfo is what Nomad knows about one of an app's volumes.
type HostVolumeInfo struct {
	// HostPath is the directory on the node that holds the volume's files.
	HostPath string
	// NodeID is the Nomad node the volume is on.
	NodeID string
}

// HostVolumes returns the host volumes Nomad has for appID, by the name the
// app gives them (without the "<app>_" prefix). The volumes panel uses it to
// show where each lives and to list the ones no service mounts any more. The
// list call doesn't carry the host path, so each volume is read once more.
func (d *Deployer) HostVolumes(ctx context.Context, appID string) (map[string]HostVolumeInfo, error) {
	prefix := nomadjob.VolumePrefix(appID)
	have, err := d.listVolumes(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make(map[string]HostVolumeInfo, len(have))
	for name, stub := range have {
		v, _, err := d.nomad.HostVolumes().Get(stub.ID, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			if statusOf(err) == http.StatusNotFound {
				continue // deleted between the list and the read
			}
			return nil, d.volumeError("read volume "+name, err)
		}
		out[strings.TrimPrefix(name, prefix)] = HostVolumeInfo{HostPath: v.HostPath, NodeID: v.NodeID}
	}
	return out, nil
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
