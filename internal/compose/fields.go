package compose

import (
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/tree"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// supported is every Compose attribute Caravana honors, exactly or with a
// warning (research R2). Anything else in a file is refused, naming its
// path: an attribute the Compose spec gains tomorrow is refused until
// someone decides what it means here. "*" matches any map key or list item;
// a pattern only matches paths of its own length, so a whole subtree is
// listed level by level (subtree).
var supported = concat(
	paths("name", "version"),

	// Services.
	paths(
		"services.*.image",
		"services.*.command", "services.*.command.*",
		"services.*.environment", "services.*.environment.*",
		"services.*.ports", "services.*.ports.*",
		"services.*.ports.*.name", "services.*.ports.*.target", "services.*.ports.*.published",
		"services.*.ports.*.host_ip", "services.*.ports.*.protocol", "services.*.ports.*.mode",
		"services.*.expose", "services.*.expose.*",
		// Volumes: the type check refuses everything but named volumes with
		// a clearer message than the attribute path would, so the defaults
		// compose-go fills in for binds and tmpfs mounts are listed too.
		"services.*.volumes", "services.*.volumes.*",
		"services.*.volumes.*.type", "services.*.volumes.*.source", "services.*.volumes.*.target",
		"services.*.volumes.*.read_only", "services.*.volumes.*.volume",
		"services.*.deploy", "services.*.deploy.replicas",
		"services.*.deploy.resources", "services.*.deploy.resources.limits",
		"services.*.deploy.resources.limits.memory", "services.*.deploy.resources.limits.cpus",
		"services.*.deploy.resources.reservations", "services.*.deploy.resources.reservations.memory",
		"services.*.scale", "services.*.mem_limit", "services.*.mem_reservation", "services.*.cpus",
		"services.*.restart", "services.*.container_name", "services.*.hostname",
		"services.*.pull_policy", "services.*.stop_grace_period",
		"services.*.networks", "services.*.networks.*",
	),
	subtree("services.*.volumes.*.bind"),
	subtree("services.*.volumes.*.tmpfs"),
	subtree("services.*.healthcheck"),
	subtree("services.*.depends_on"),
	subtree("services.*.labels"),
	subtree("services.*.logging"),

	// Top-level volumes and networks: declared, with nothing but a name.
	paths("volumes", "volumes.*", "volumes.*.name"),
	paths("networks", "networks.*", "networks.*.name"),
)

func paths(ps ...string) []tree.Path {
	out := make([]tree.Path, len(ps))
	for i, p := range ps {
		out[i] = tree.Path(p)
	}
	return out
}

// subtree is p and everything up to three levels under it.
func subtree(p string) []tree.Path {
	return paths(p, p+".*", p+".*.*", p+".*.*.*")
}

func concat(lists ...[]tree.Path) []tree.Path {
	var out []tree.Path
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// hints say what to do instead, for the attributes refused most often, by
// the attribute's name (the last part of its path).
var hints = map[string]string{
	"build":        "Caravana deploys prebuilt images: push the image to a registry and use image:",
	"entrypoint":   "isn't supported yet: bake it into the image, or use command:",
	"env_file":     "Caravana doesn't read files from your disk: put the values in environment:, or send secret values in the request's secrets",
	"secrets":      "Caravana doesn't read files from your disk: send secret values in the request's services.<name>.secrets",
	"configs":      "Caravana doesn't read files from your disk: bake the file into the image",
	"include":      "Caravana doesn't read other files: put every service in one file",
	"extends":      "Caravana doesn't read other files: write the service out in full",
	"profiles":     "every service of the file is deployed: remove the services you don't want, or the profiles",
	"privileged":   "isn't allowed: Caravana runs apps without extra privileges",
	"cap_add":      "isn't allowed: Caravana runs apps without extra privileges",
	"devices":      "isn't allowed: Caravana runs apps without host devices",
	"network_mode": "isn't supported: every service gets its own network namespace, and reaches the others by name",
	"links":        "isn't needed: every service reaches the others by name",
	"extra_hosts":  "isn't supported: Caravana manages the hosts file so services find each other by name",
	"user":         "isn't supported yet: set the user in the image",
	"working_dir":  "isn't supported yet: set the working directory in the image",
	"driver":       "isn't supported: volumes are directories on the node, created by Caravana",
	"driver_opts":  "isn't supported: volumes are directories on the node, created by Caravana",
	"external":     "isn't supported: Caravana creates every volume of the app",
	"nocopy":       "isn't supported: a new volume starts empty",
}

// refused is the error for an attribute outside the allowlist.
func refused(u loader.UnsupportedAttribute) error {
	field := filePath(u.Path)
	parts := u.Path.Parts()
	name := parts[len(parts)-1]
	if hint, ok := hints[name]; ok {
		return app.NewFieldError(field, "%s", hint)
	}
	return app.NewFieldError(field, "isn't supported by Caravana")
}

// filePath writes a compose-go path as it reads in a file:
// "services.web.ports.[].protocol" becomes "services.web.ports[].protocol".
func filePath(p tree.Path) string {
	return strings.ReplaceAll(p.String(), ".[]", "[]")
}

// projectWarnings are the warnings about the file as a whole.
func projectWarnings(p *types.Project) []app.Warning {
	var out []app.Warning
	for _, name := range sortedKeys(p.Networks) {
		if name != "default" {
			out = append(out, app.Warning{Field: "networks." + name,
				Message: "networks don't separate services here: every service of the app reaches the others by name"})
		}
	}
	for _, key := range sortedKeys(p.Volumes) {
		v := p.Volumes[key]
		if v.Name != "" && v.Name != p.Name+"_"+key {
			out = append(out, app.Warning{Field: "volumes." + key + ".name",
				Message: fmt.Sprintf("is ignored: the volume is the app's own %q", key)})
		}
	}
	return out
}
