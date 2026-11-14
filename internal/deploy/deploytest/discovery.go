package deploytest

import "net/http"

// discoveryAnswer is what the fake serves at one of Nomad's discovery paths.
type discoveryAnswer struct {
	status int
	body   []byte
}

// SetDiscovery sets what GET path answers on this Nomad, for the paths Nomad serves outside /v1
// (/.well-known/openid-configuration and /.well-known/jwks.json, spec 038). Before it is called a
// path answers 404, as a Nomad without an issuer does for the configuration.
func (n *Nomad) SetDiscovery(path string, status int, body []byte) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.discovery == nil {
		n.discovery = map[string]discoveryAnswer{}
	}
	n.discovery[path] = discoveryAnswer{status: status, body: body}
}

// DiscoveryRequests is how many times each discovery path was asked for.
func (n *Nomad) DiscoveryRequests(path string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.discoveryAsked[path]
}

func (n *Nomad) serveDiscovery(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	if n.discoveryAsked == nil {
		n.discoveryAsked = map[string]int{}
	}
	n.discoveryAsked[r.URL.Path]++
	ans, ok := n.discovery[r.URL.Path]
	n.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(ans.status)
	_, _ = w.Write(ans.body)
}
