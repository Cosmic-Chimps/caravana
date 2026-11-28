package ui

import (
	"context"
	"net/url"
	"slices"
	"strconv"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Names across apps (task 2xg2): where a service answers by name, drawn on Configure's Overview, the
// Running tab and the template's installed notice. The rule is app.Addresses; this file words it.

// addrRow is one service's names, worded. Service is drawn, on a line of its own, only when Many: the
// app has more than one service with ports. Overview names the service in its own row, so it clears Many.
type addrRow struct {
	Service string
	Many    bool
	// Lines are the sentences, each a run of parts.
	Lines [][]addrPart
}

// addrPart is a piece of a sentence: plain text, a name in mono (Copy: a name that works, one click
// selects it) or a link to another app.
type addrPart struct {
	Text string
	Mono bool
	Copy bool
	Href string
}

func aplain(t string) addrPart { return addrPart{Text: t} }
func amono(t string) addrPart  { return addrPart{Text: t, Mono: true} }

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

// addrRowsFrom is addrRowsOf from the addresses. The sentences:
//
//	Other apps in <place> reach it at `a:80` or `b:80`.            (names that work, shortest first)
//	The other services of this app reach it at `web:80`.           (a multi-service app)
//	`b` reaches neither app: it is also the name of <claim>.       (a name another app claims too)
//
// and when no name works, one sentence for all of them (see unreachable).
func addrRowsFrom(addrs []app.Address, place string) []addrRow {
	var rows []addrRow
	for _, a := range addrs {
		if len(a.Ports) == 0 {
			continue
		}
		port := ":" + strconv.Itoa(a.Ports[0])
		row := addrRow{Service: a.Service, Many: len(addrs) > 1}
		names := slices.Clone(a.Names)
		slices.SortStableFunc(names, func(x, y string) int { return len(x) - len(y) })
		conflicts := conflictsByName(a.Conflicts)
		switch {
		case len(names) > 0:
			line := []addrPart{aplain("Other apps in " + place + " reach it at ")}
			for i, n := range names {
				if i > 0 {
					line = append(line, aplain(" or "))
				}
				line = append(line, addrPart{Text: n + port, Mono: true, Copy: true})
			}
			row.Lines = append(row.Lines, append(line, aplain(".")))
		case len(conflicts) > 0:
			row.Lines = append(row.Lines, unreachable(place, conflicts))
		}
		if a.Own != "" {
			row.Lines = append(row.Lines, []addrPart{aplain("The other services of this app reach it at "),
				{Text: a.Own + port, Mono: true, Copy: true}, aplain(".")})
		}
		if len(names) > 0 {
			for _, c := range conflicts {
				line := []addrPart{amono(c.name)}
				if len(c.by) == 1 {
					line = append(line, aplain(" reaches neither app: it is also the name of "))
				} else {
					line = append(line, aplain(" reaches none of them: it is also the name of "))
				}
				row.Lines = append(row.Lines, append(append(line, claimsOf(c.by)...), aplain(".")))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// nameClaim is a name the service claims and the other services that claim it too.
type nameClaim struct {
	name string
	by   []app.NameConflict
}

// conflictsByName groups the conflicts by name, in the order the names first appear.
func conflictsByName(cs []app.NameConflict) []nameClaim {
	var out []nameClaim
	for _, c := range cs {
		if n := len(out); n > 0 && out[n-1].name == c.Name {
			out[n-1].by = append(out[n-1].by, c)
			continue
		}
		out = append(out, nameClaim{name: c.Name, by: []app.NameConflict{c}})
	}
	return out
}

// claimsOf words the other claimants of one name: "the app <link>" when the name is that app's own
// ("postgres-postgres" is the app of that name), else "`web` in <link>", joined by commas and "and".
func claimsOf(by []app.NameConflict) []addrPart {
	var out []addrPart
	for i, c := range by {
		switch {
		case i == 0:
		case i == len(by)-1:
			out = append(out, aplain(" and "))
		default:
			out = append(out, aplain(", "))
		}
		link := addrPart{Text: app.HostAppName(c.App), Href: "/apps/" + url.PathEscape(c.App)}
		if c.Name == app.HostAppName(c.App) {
			out = append(out, aplain("the app "), link)
		} else {
			out = append(out, amono(c.Service), aplain(" in "), link)
		}
	}
	return out
}

// unreachable is the one sentence for a service none of whose names works: what collides, with whom,
// and what frees it.
func unreachable(place string, cs []nameClaim) []addrPart {
	line := []addrPart{aplain("Other apps in " + place + " can't reach it by name: ")}
	pairs := true
	for i, c := range cs {
		if i > 0 {
			line = append(line, aplain("; "))
		}
		line = append(line, amono(c.name), aplain(" is also the name of "))
		line = append(line, claimsOf(c.by)...)
		pairs = pairs && len(c.by) == 1
	}
	switch {
	case len(cs) == 1 && pairs:
		return append(line, aplain(". Rename one of the two apps to free it."))
	case len(cs) == 1:
		return append(line, aplain(". Rename all but one of these apps to free it."))
	case pairs:
		return append(line, aplain(". Renaming one app of each pair frees that name."))
	}
	return append(line, aplain(". Renaming apps until each name has one owner frees them."))
}
