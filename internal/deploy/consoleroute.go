package deploy

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/hashicorp/nomad/api"
)

// The console's route in Traefik (feature 051). The console's own domain is not an app's: it is a
// static route of Traefik's file provider, rendered from the template caravana.yml of Traefik's
// job. That template reads the item "hosts" of the Nomad Variable below, a space-separated list of
// the domains that reach Caravana (the current one, a pending new one and the old ones still
// redirecting), and falls back to the job's own console_domain variable when there is none. The
// template is noop: Traefik's file provider sees the new file without a restart, so a change of
// address never costs a gap on 80 and 443.

// ConsoleHostsPath is the Nomad Variable that holds the console's hosts: the group path of Traefik's
// job, which the job's workload identity reads with no policy line (a job reads its own paths). It
// is not TraefikVariablePath, which belongs to the Team-only users.
const ConsoleHostsPath = "nomad/jobs/traefik/traefik"

// consoleHostsItem is the item of ConsoleHostsPath that holds the list.
const consoleHostsItem = "hosts"

// ConsoleRouteSupported says whether Traefik's job renders the console's route from ConsoleHostsPath.
// A host the installer made before feature 051 has the job without it, and the console's address
// can't change until the installer runs again (which restarts Traefik once). A host with no job named
// traefik is ErrNoProxy.
func (d *Deployer) ConsoleRouteSupported(ctx context.Context) (bool, error) {
	job, _, err := d.nomad.Jobs().Info(ProxyJob, (&api.QueryOptions{}).WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		if statusOf(err) == http.StatusNotFound {
			return false, ErrNoProxy
		}
		return false, d.callError("read the traefik job", err)
	}
	for _, g := range job.TaskGroups {
		if g == nil {
			continue
		}
		for _, t := range g.Tasks {
			if t == nil {
				continue
			}
			for _, tmpl := range t.Templates {
				if tmpl != nil && tmpl.EmbeddedTmpl != nil && strings.Contains(*tmpl.EmbeddedTmpl, ConsoleHostsPath) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// consoleHostRE is what a host may be: it lands in Traefik's template, inside a Host(`...`) rule, so
// nothing that could end the rule or the line gets there. The service has already checked the names as
// host names; this is the last gate before the variable.
var consoleHostRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// SetConsoleHosts makes the console's route serve exactly hosts: it writes them, space-separated, to
// the item "hosts" of ConsoleHostsPath, and Traefik's template turns them into one router each. The
// caller gives lowercase host names only (the service has checked them); a name with a space in it
// or any other character is refused here too (see consoleHostRE). An empty list deletes the variable, and the
// route goes back to the job's own console_domain.
func (d *Deployer) SetConsoleHosts(ctx context.Context, hosts []string) error {
	if len(hosts) == 0 {
		return d.deleteVariable(ctx, ConsoleHostsPath)
	}
	for _, h := range hosts {
		if !consoleHostRE.MatchString(h) {
			return errors.New("set the console hosts: a host is lower case letters, digits, dashes and dots")
		}
	}
	return d.writeVariable(ctx, ConsoleHostsPath, map[string]string{consoleHostsItem: strings.Join(hosts, " ")})
}
