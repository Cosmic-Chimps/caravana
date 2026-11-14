package nomadjob

import (
	"time"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// workloadIdentities is the service's workload identities as the task's
// identity blocks (spec 038): one per identity, in order, each written to a
// file (Nomad rewrites it when it renews the token). A service without
// identities gets nil, so its job is unchanged.
func workloadIdentities(svc app.Service) []*api.WorkloadIdentity {
	if len(svc.Identities) == 0 {
		return nil
	}
	out := make([]*api.WorkloadIdentity, len(svc.Identities))
	for i, id := range svc.Identities {
		out[i] = &api.WorkloadIdentity{
			Name:     id.Name,
			Audience: append([]string(nil), id.Audience...),
			TTL:      time.Duration(id.TTLOrDefault()) * time.Second,
			File:     true,
		}
	}
	return out
}
