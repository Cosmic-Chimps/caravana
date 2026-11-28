package ui

import (
	"context"
	"net/url"
	"strconv"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Names across apps (task 2xg2): where a service answers by name, drawn on Configure's Overview, the
// Running tab and the template's installed notice. The rule is app.Addresses; this file words it.

// addrRow is one service's names. Service is drawn only when the app has more than one row.
type addrRow struct {
	Service string
	Many    bool
	// Place is where the other apps are: "Shop › production", or "this environment" when the
	// installation has a single project and environment.
	Place string
	// Names are what other apps reach it at, each as "name:port".
	Names []string
	// Own is how the app's other services reach it ("web:8080"); "" for a one-service app.
	Own string
	// NoReach says every name it claims collides, so no other app can reach it by name.
	NoReach   bool
	Conflicts []addrConflict
}

// addrConflict is a name the service claims that another app's service claims too.
type addrConflict struct {
	Name, App, AppHref, Service string
}

// addrRowsOf words the addresses of the app rec: nil for an app with no service that has ports, or
// when the addresses can't be read (they are a nicety: the page draws without them).
func (s *server) addrRowsOf(ctx context.Context, rec store.AppRecord) []addrRow {
	addrs, err := s.svc.AppAddresses(ctx, rec)
	if err != nil {
		s.log.Warn("read the names of an app's services", "app", rec.App.ID, "error", err)
		return nil
	}
	place := "this environment"
	if pl, err := s.loadPlaces(ctx); err == nil && !pl.single() {
		place = pl.display(rec.Place.Project) + " › " + rec.Place.Environment
	}
	return addrRowsFrom(addrs, place)
}

// addrRowsFrom is addrRowsOf from the addresses.
func addrRowsFrom(addrs []app.Address, place string) []addrRow {
	var rows []addrRow
	for _, a := range addrs {
		if len(a.Ports) == 0 {
			continue
		}
		port := ":" + strconv.Itoa(a.Ports[0])
		row := addrRow{Service: a.Service, Many: len(addrs) > 1, Place: place, NoReach: len(a.Names) == 0 && len(a.Conflicts) > 0}
		for _, n := range a.Names {
			row.Names = append(row.Names, n+port)
		}
		if a.Own != "" {
			row.Own = a.Own + port
		}
		for _, c := range a.Conflicts {
			row.Conflicts = append(row.Conflicts, addrConflict{Name: c.Name, App: app.HostAppName(c.App),
				AppHref: "/apps/" + url.PathEscape(c.App), Service: c.Service})
		}
		rows = append(rows, row)
	}
	return rows
}
