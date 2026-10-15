package deploy

import (
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
)

// Export returns deployment dep of app a as a Nomad jobspec in HCL: the job
// Register submitted for it, after a header saying what the job needs from
// the cluster. deployed is when the deployment was made. It reads nothing
// from Nomad, and the file holds no secret value: secrets reach the job
// through a Nomad Variable, which the header names.
//
// simplified: it uses today's settings (Traefik entrypoint, cert resolver,
// names image), not the ones of the deployment's time. They change only
// when the operator reconfigures Caravana; if that matters, store the
// settings with each deployment.
func (d *Deployer) Export(a app.App, dep app.Deployment, deployed time.Time) ([]byte, error) {
	res, err := nomadjob.Translate(a.WithoutSecretValues(), dep, d.settings)
	if err != nil {
		return nil, fmt.Errorf("export deployment %q of %q: %w", dep.ID, a.ID, err)
	}
	return nomadjob.HCL(res.Job, exportHeader(a, dep, deployed, d.settings)), nil
}

// exportHeader says where the file comes from and what the job needs from
// the cluster: routing, host volumes and secrets.
func exportHeader(a app.App, dep app.Deployment, deployed time.Time, s nomadjob.Settings) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Nomad job for app %q, exported by Caravana.\n", a.ID)
	fmt.Fprintf(&b, "Deployment %s, deployed %s.\n", dep.ID, deployed.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Run it with: nomad job run %s.nomad.hcl\n", a.ID)
	b.WriteString("\nWhat it needs from the cluster:\n")

	var routes, volumes, volumeVars, secrets, backups []string
	for _, svc := range a.Services {
		for _, dom := range svc.Domains {
			routes = append(routes, fmt.Sprintf("  %s -> service %s, port %s", dom.Host, svc.Name, dom.Port))
		}
		for _, v := range svc.Volumes {
			volumes = append(volumes, fmt.Sprintf("- Host volume %q, mounted by service %s.", nomadjob.VolumeName(a.ID, v.Name), svc.Name))
		}
		if len(svc.Volumes) > 0 {
			// Without the variable, the caravana-volumes task waits for it
			// and the service never starts.
			path := nomadjob.VolumeVariablePath(a.ID, svc.Name)
			volumeVars = append(volumeVars, fmt.Sprintf(
				"- Nomad Variable %s, which the %s task of service %s reads before the service starts.\n"+
					"  Create it before running the job, e.g. nomad var put %s LIMIT=1800",
				path, nomadjob.VolumeTaskName, svc.Name, path))
		}
		if len(svc.Secrets) > 0 {
			names := make([]string, len(svc.Secrets))
			for i, sec := range svc.Secrets {
				names[i] = sec.Name
			}
			secrets = append(secrets, fmt.Sprintf("- Nomad Variable %s with the secrets %s.",
				nomadjob.SecretsPath(a.ID, svc.Name), strings.Join(names, ", ")))
		}
		if svc.Backup != nil {
			backups = append(backups, nomadjob.BackupJobID(a.ID, svc.Name))
		}
	}

	if len(routes) > 0 {
		resolver := "Traefik's default certificate"
		if s.CertResolver != "" {
			resolver = fmt.Sprintf("cert resolver %q", s.CertResolver)
		}
		fmt.Fprintf(&b, "- Traefik with its Nomad provider, entrypoint %q, %s, routing:\n", s.Entrypoint, resolver)
		for _, r := range routes {
			b.WriteString(r + "\n")
		}
	}
	for _, v := range volumes {
		b.WriteString(v + "\n")
	}
	for _, v := range volumeVars {
		b.WriteString(v + "\n")
	}
	for _, s := range secrets {
		b.WriteString(s + "\n")
	}
	if len(secrets) > 0 {
		b.WriteString("  Their values are not in this file: they stay in Nomad.\n")
	}
	if len(routes)+len(volumes)+len(secrets) == 0 {
		b.WriteString("- Nothing beyond a Nomad client with Docker.\n")
	}
	if len(backups) > 0 {
		fmt.Fprintf(&b, "\nBackups run as separate Nomad jobs (%s), not part of this file.\n", strings.Join(backups, ", "))
	}
	return b.String()
}
