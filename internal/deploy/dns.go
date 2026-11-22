package deploy

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// DNS providers (feature 048). A zone's API token lives in a Nomad Variable of
// Caravana's own (constitution VII), never in SQLite: the registries' and the
// secret providers' credentials are kept the same way.

// dnsZoneVariablePrefix is where the tokens are kept, one variable per zone.
const dnsZoneVariablePrefix = "caravana/dns/"

// DNSZoneToken is the item of a zone's variable that holds the token.
const DNSZoneToken = "token"

// zoneName is a lowercase domain name with at least one dot.
var zoneName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// DNSZoneVariablePath is the Nomad Variable that holds the token of a zone:
// "caravana/dns/example~com" for "example.com" ("." is not a legal character
// of a variable path, so it is written "~", as the registries' hosts are).
// A zone holds no "~" itself, so two zones never share a path.
func DNSZoneVariablePath(zone string) string {
	return dnsZoneVariablePrefix + strings.ReplaceAll(zone, ".", "~")
}

// checkDNSZone refuses a name that could change which variable the path
// names. Zones are checked at the door, so this guards against a bug.
func checkDNSZone(zone string) error {
	if len(zone) > 253 || !zoneName.MatchString(zone) {
		return fmt.Errorf("dns zone %q is not a valid variable name", zone)
	}
	return nil
}

// DNSZoneTokenFor returns the token stored for a zone. ok is false when there
// is none. Errors name the action, never the value.
func (d *Deployer) DNSZoneTokenFor(ctx context.Context, zone string) (token string, ok bool, err error) {
	if err := checkDNSZone(zone); err != nil {
		return "", false, err
	}
	items, found, err := d.readVariableIfAny(ctx, DNSZoneVariablePath(zone))
	if err != nil {
		return "", false, err // names the variable already
	}
	if !found || items[DNSZoneToken] == "" {
		return "", false, nil
	}
	return items[DNSZoneToken], true, nil
}

// SaveDNSZoneToken keeps token as the zone's, replacing the one before. An
// empty token is refused: Nomad refuses a variable with every item empty.
func (d *Deployer) SaveDNSZoneToken(ctx context.Context, zone, token string) error {
	if err := checkDNSZone(zone); err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("dns zone %q: the token is empty", zone)
	}
	return d.writeVariable(ctx, DNSZoneVariablePath(zone), map[string]string{DNSZoneToken: token})
}

// DeleteDNSZoneToken forgets a zone's token. A zone with none is not an error.
func (d *Deployer) DeleteDNSZoneToken(ctx context.Context, zone string) error {
	if err := checkDNSZone(zone); err != nil {
		return err
	}
	return d.deleteVariable(ctx, DNSZoneVariablePath(zone))
}
