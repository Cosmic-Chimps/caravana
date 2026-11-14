package ui

import (
	"strconv"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
)

// identityView is one workload identity of a service as Configure shows it,
// read-only (spec 038): the console never edits identities, and never holds the
// token, only the name of the file a copy reads it from.
type identityView struct {
	Name     string
	Audience []string
	TTL      string // in words: "1 hour", "2 minutes"
	File     string // where the copy reads the token
	Bella    bool   // the audience is Bella Baxter's
}

// identitiesOf is the service's identities as Configure shows them.
func identitiesOf(svc app.Service) []identityView {
	var out []identityView
	for _, id := range svc.Identities {
		v := identityView{
			Name:     id.Name,
			Audience: id.Audience,
			TTL:      durationWords(id.TTLOrDefault()),
			File:     "/secrets/nomad_" + id.Name + ".jwt",
		}
		for _, aud := range id.Audience {
			if strings.HasPrefix(aud, "bella-spiffe") {
				v.Bella = true
			}
		}
		out = append(out, v)
	}
	return out
}

// durationWords says a number of seconds the way a person would: "1 hour",
// "2 minutes", "1 minute 30 seconds".
func durationWords(seconds int) string {
	var parts []string
	for _, unit := range []struct {
		name string
		secs int
	}{{"hour", 3600}, {"minute", 60}, {"second", 1}} {
		n := seconds / unit.secs
		seconds %= unit.secs
		switch {
		case n == 1:
			parts = append(parts, "1 "+unit.name)
		case n > 1:
			parts = append(parts, strconv.Itoa(n)+" "+unit.name+"s")
		}
	}
	return strings.Join(parts, " ")
}
