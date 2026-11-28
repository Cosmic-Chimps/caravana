package selfbackup

import "strings"

// Keep says whether the Nomad Variable at path is part of a backup of Caravana (plan D1).
//
//   - caravana/*, except caravana/cluster/*: the cluster's CA and gossip key belong to the cluster the
//     installer makes, and the restored store's servers join that one again.
//   - nomad/jobs/<app>/<service>: exactly two segments, an app ID without "_" (helper jobs have one) that
//     is not traefik, and a service that is not "caravana" (nomad/jobs/<app>/caravana/previous belongs to
//     a deployment in flight). These are the secret sets and the secret files, the only copy of a kept
//     secret value.
//
// Everything else is written again by the next deploy from the store and the variables above: registry
// copies (<app>/<svc>/<svc>), volume requests, helper jobs' variables and Traefik's.
func Keep(path string) bool {
	if rest, ok := strings.CutPrefix(path, "caravana/"); ok {
		return rest != "" && rest != "cluster" && !strings.HasPrefix(rest, "cluster/")
	}
	rest, ok := strings.CutPrefix(path, "nomad/jobs/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return false
	}
	app, service := parts[0], parts[1]
	return app != "" && service != "" && !strings.Contains(app, "_") && app != "traefik" && service != "caravana"
}
