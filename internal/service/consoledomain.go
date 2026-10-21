package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// consoleHost is the host of the console's own address (CARAVANA_PUBLIC_URL):
// lowercase, without the port and without a trailing dot. It is "" when no
// address is set (local development) or it has no host, and then nothing is
// compared.
func (s *Service) consoleHost() string {
	u, err := url.Parse(s.publicURL)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
}

// checkConsoleDomain refuses an app that routes the console's own host: Traefik
// would then have two routers for one Host rule, the installer's static route
// to the console and the app's, and the console stopped answering while the app
// rolled out. It is a *app.FieldError naming the first such domain, as the other
// domain checks are. Every path that saves or deploys an app's domains calls it
// after validation, including the redeploy of a stored app, so an app saved
// before this check is refused on its next deploy rather than keeping the clash.
func (s *Service) checkConsoleDomain(a app.App) error {
	console := s.consoleHost()
	if console == "" {
		return nil
	}
	for i, svc := range a.Services {
		for j, dom := range svc.Domains {
			if strings.TrimSuffix(strings.ToLower(dom.Host), ".") == console {
				return app.NewFieldError(fmt.Sprintf("services[%d].domains[%d].host", i, j),
					"%s is the console's own address (CARAVANA_PUBLIC_URL): an app can't use it, or the console would stop answering. Use another domain.", dom.Host)
			}
		}
	}
	return nil
}
