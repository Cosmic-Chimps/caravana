package app

import (
	"fmt"
	"slices"
)

// validProtocol reports whether p is one of the values of Port.Protocol,
// the empty one included.
func validProtocol(p string) bool {
	switch p {
	case "", ProtocolTCP, ProtocolUDP, ProtocolTCPUDP:
		return true
	}
	return false
}

// validatePublished checks the published ports of one service (spec 041).
// published collects, across the whole app, which service and port holds each
// number, so a second claim is refused naming the first.
func validatePublished(field string, svc Service, published map[int]string) error {
	count := 0
	for j, p := range svc.Ports {
		if !p.IsPublished() {
			continue
		}
		count++
		f := fmt.Sprintf("%s.ports[%d].published", field, j)
		if p.Published < 1 || p.Published > 65535 {
			return NewFieldError(f, "must be between 1 and 65535, got %d (leave it out to not publish the port)", p.Published)
		}
		if slices.Contains(ReservedPorts, p.Published) {
			return NewFieldError(f, "%d is reserved: %s", p.Published, reservedPortUse(p.Published))
		}
		if first, ok := published[p.Published]; ok {
			return NewFieldError(f, "%d is already published by %s", p.Published, first)
		}
		published[p.Published] = fmt.Sprintf("service %q, port %q", svc.Name, p.Name)
	}
	if count > MaxPublishedPorts {
		return NewFieldError(field+".ports", "publishes %d ports; at most %d are allowed in one service", count, MaxPublishedPorts)
	}
	if count > 0 && svc.Replicas != nil && *svc.Replicas > 1 {
		return NewFieldError(field+".replicas", "a service with a published port runs one copy: one address can hold a fixed port once, got %d", *svc.Replicas)
	}
	return nil
}

// validateTCPUse refuses a domain on a port that doesn't speak TCP:
// Traefik reaches a service over TCP.
func validateTCPUse(field string, svc Service) error {
	udpOnly := map[string]bool{}
	for _, p := range svc.Ports {
		if !p.SpeaksTCP() {
			udpOnly[p.Name] = true
		}
	}
	for j, dom := range svc.Domains {
		if udpOnly[dom.Port] {
			return NewFieldError(fmt.Sprintf("%s.domains[%d].port", field, j), "%q is a UDP port: a domain is served over TCP", dom.Port)
		}
	}
	return nil
}
