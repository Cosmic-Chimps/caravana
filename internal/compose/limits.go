package compose

import (
	"strconv"

	"github.com/compose-spec/compose-go/v2/types"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// ulimits maps services.*.ulimits (a number, or soft and hard) to the model's. Only memlock and nofile are
// honored; any other name is refused at services.<svc>.ulimits.<name>, and so is a value the model refuses,
// naming the path as written.
func ulimits(field string, sc types.ServiceConfig) (map[string]app.Ulimit, error) {
	if len(sc.Ulimits) == 0 {
		return nil, nil
	}
	out := make(map[string]app.Ulimit, len(sc.Ulimits))
	for _, name := range sortedKeys(sc.Ulimits) {
		var u types.UlimitsConfig
		if c := sc.Ulimits[name]; c != nil {
			u = *c
		}
		// "nofile: 20000" is the same number soft and hard. compose-go keeps it apart from soft and hard,
		// and a zero in both of those is the number form of 0.
		single := u.Single != 0 || (u.Soft == 0 && u.Hard == 0)
		limit := app.Ulimit{Soft: int64(u.Soft), Hard: int64(u.Hard)}
		if single {
			limit = app.Ulimit{Soft: int64(u.Single), Hard: int64(u.Single)}
		}
		if err := app.CheckUlimit(field+".ulimits."+name, name, limit, single); err != nil {
			return nil, err
		}
		out[name] = limit
	}
	return out, nil
}

// sysctls maps services.*.sysctls (a map, or a list of name=value) to the model's. A namespaced net.* setting
// is honored. vm.max_map_count, which Elasticsearch and OpenSearch files carry, is a setting of the server
// that the installer already makes 262144: asking for that or less is dropped with a warning (the host
// already satisfies it), asking for more is refused. Any other name is refused at
// services.<svc>.sysctls.<name>.
func sysctls(field string, sc types.ServiceConfig, warn func(f, format string, args ...any)) (map[string]string, error) {
	if len(sc.Sysctls) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(sc.Sysctls))
	for _, key := range sortedKeys(sc.Sysctls) {
		value := sc.Sysctls[key]
		if key != app.MapCountKey {
			out[key] = value
			continue
		}
		f := field + ".sysctls." + key
		n, err := strconv.ParseInt(value, 10, 64)
		switch {
		case err != nil:
			return nil, app.NewFieldError(f, "%q isn't a number", value)
		case n > app.MinMapCount:
			return nil, app.NewFieldError(f, "%d is more than the %d every server has: it is a setting of the server, and a container can't set it", n, app.MinMapCount)
		}
		warn(".sysctls."+key, "set on every server by the installer (%d); a container can't set it", app.MinMapCount)
	}
	if len(out) == 0 {
		return nil, nil
	}
	if err := app.CheckSysctls(field+".sysctls", out); err != nil {
		return nil, err
	}
	return out, nil
}
