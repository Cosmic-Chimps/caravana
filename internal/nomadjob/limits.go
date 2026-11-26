package nomadjob

import (
	"github.com/cosmic-chimps/caravana/internal/app"
)

// withLimits puts the service's ulimits and sysctls in the docker driver's config (feature 060). In a JSON
// job the driver takes each as a list holding one map of strings, like auth: {"nofile": "65536:65536"}.
// A service with neither gets config back unchanged, so its job is too.
func withLimits(config map[string]any, svc app.Service) map[string]any {
	if len(svc.Ulimits) > 0 {
		limits := make(map[string]any, len(svc.Ulimits))
		for name, u := range svc.Ulimits {
			limits[name] = u.String()
		}
		config["ulimit"] = []map[string]any{limits}
	}
	if len(svc.Sysctls) > 0 {
		sysctls := make(map[string]any, len(svc.Sysctls))
		for k, v := range svc.Sysctls {
			sysctls[k] = v
		}
		config["sysctl"] = []map[string]any{sysctls}
	}
	return config
}
