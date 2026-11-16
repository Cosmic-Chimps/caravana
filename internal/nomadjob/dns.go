package nomadjob

import (
	"slices"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// dnsConfig is the service's resolver settings as the group network's dns
// block, which Nomad writes to the task's resolv.conf. A service without any
// gets nil, so its job is unchanged.
func dnsConfig(svc app.Service) *api.DNSConfig {
	if svc.DNS == nil || svc.DNS.Empty() {
		return nil
	}
	return &api.DNSConfig{
		Servers:  slices.Clone(svc.DNS.Servers),
		Searches: slices.Clone(svc.DNS.Searches),
		Options:  slices.Clone(svc.DNS.Options),
	}
}
