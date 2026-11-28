package deploy

import (
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/nomad/api"

	"github.com/cosmic-chimps/caravana/internal/selfbackup"
)

// Caravana's own backup (feature 063) reads the Nomad Variables it owns and, on a restore, writes them
// again. Which variables belong is selfbackup.Keep's rule; this file only reaches Nomad.

// variablePrefixes are the two roots of Caravana's variables: its own settings and credentials, and the
// per-service secret sets of its apps (and the helper jobs' variables, which Keep drops).
var variablePrefixes = []string{"caravana/", "nomad/jobs/"}

// ExportVariables returns every variable under Caravana's two prefixes for which keep says yes, with its
// items, sorted by path. A variable deleted between the listing and the read is skipped. The items are
// secrets: the caller keeps them in memory and encrypts them.
func (d *Deployer) ExportVariables(ctx context.Context, keep func(path string) bool) ([]selfbackup.Variable, error) {
	var paths []string
	for _, prefix := range variablePrefixes {
		metas, _, err := d.nomad.Variables().PrefixList(prefix, (&api.QueryOptions{}).WithContext(ctx))
		if err != nil {
			return nil, d.volumeError("list the variables under "+prefix, err)
		}
		for _, m := range metas {
			if keep(m.Path) {
				paths = append(paths, m.Path)
			}
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	out := make([]selfbackup.Variable, 0, len(paths))
	for _, path := range paths {
		items, found, err := d.readVariableIfAny(ctx, path)
		if err != nil {
			return nil, err
		}
		if !found || len(items) == 0 {
			continue
		}
		out = append(out, selfbackup.Variable{Path: path, Items: items})
	}
	return out, nil
}

// ImportVariables writes variables back, replacing any that exist. It refuses the whole list, writing
// nothing, when a path isn't one a backup keeps (a backup that names Traefik's or the cluster's variables
// is not one of ours) or has no items (Nomad refuses such a variable). It returns how many it wrote.
func (d *Deployer) ImportVariables(ctx context.Context, vars []selfbackup.Variable) (int, error) {
	for _, v := range vars {
		if !selfbackup.Keep(v.Path) {
			return 0, fmt.Errorf("import variables: %q is not a variable a backup of Caravana keeps", v.Path)
		}
		if len(v.Items) == 0 {
			return 0, fmt.Errorf("import variables: %q has no items", v.Path)
		}
	}
	n := 0
	for _, v := range vars {
		if err := d.writeVariable(ctx, v.Path, v.Items); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
