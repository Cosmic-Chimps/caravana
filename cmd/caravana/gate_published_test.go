package main

// The published-ports gate step (spec 041): a service's fixed TCP and UDP port opens on the
// address of the server that runs it, and closes when the app is gone. It is in its own file only
// because gate_test.go is long; runGate lists it with the other steps.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// publishedPort is a high port nothing in the lab or on a host uses.
const publishedPort = 34478

// publishedEcho is the app the step publishes: socat answers "pong-tcp" to a TCP connection and
// "pong-udp" to a UDP datagram on container port 4000, which is published as publishedPort. The
// health check is TCP on that same port.
func publishedEcho(port int) string {
	script := `socat UDP-LISTEN:4000,fork,reuseaddr SYSTEM:'echo pong-udp' & exec socat TCP-LISTEN:4000,fork,reuseaddr SYSTEM:'echo pong-tcp'`
	body, _ := json.Marshal(map[string]any{
		"source": map[string]string{"kind": "image"},
		"services": []any{map[string]any{
			"name": "echo", "image": "alpine/socat:latest",
			"entrypoint": []string{"sh", "-c"}, "args": []string{script},
			"ports":     []any{map[string]any{"name": "turn", "containerPort": 4000, "protocol": "tcp+udp", "published": port}},
			"health":    map[string]any{"interval": "5s", "timeout": "2s"},
			"resources": map[string]any{"memoryMB": 16, "memoryMaxMB": 32},
		}},
	})
	return string(body)
}

// publishedClient is a copy that stays running, from which the step reaches the published port:
// the lab's VM address can't be reached from the Mac.
const publishedClient = `{
  "source": {"kind": "image"},
  "services": [{"name": "client", "image": "busybox:1.37", "command": "sh",
    "args": ["-c", "trap 'exit 0' TERM; while :; do sleep 3600 & wait $!; done"],
    "resources": {"memoryMB": 16, "memoryMaxMB": 32}}]
}`

// publishedPortsStep (spec 041):
//
//  1. refusals that name their field, before anything deploys: a reserved port, a second copy, a
//     health check on a UDP-only port;
//  2. the echo app deploys healthy, and its job has a static port on the host network "public"
//     (the group runs one copy and deploys stop-then-start);
//  3. from another copy, the server's address answers over TCP and UDP (and from this process on a
//     real host, through the firewall), and still does after a redeploy replaced the copy;
//  4. another app publishing the same number is 409 port_taken on the field;
//  5. the export plans against the live job with no diff;
//  6. deleting the app closes the port.
func (g *gate) publishedPortsStep(t *testing.T) {
	const echoID, clientID, otherID = "gate-pub", "gate-pub-client", "gate-pub2"
	ids := []string{echoID, clientID, otherID}
	remove := func() {
		for _, id := range ids {
			if resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+id, "", ""); err == nil {
				resp.Body.Close()
			}
		}
	}
	remove() // leftovers from an aborted run, if any
	for _, id := range ids {
		waitFor(t, id+"'s Nomad job to be gone", time.Minute, func() bool {
			_, _, err := g.target.Nomad.Jobs().Info(id, nil)
			return err != nil
		})
	}
	t.Cleanup(remove)
	post := func(id, body string) (int, []byte) {
		t.Helper()
		return g.send(t, http.MethodPost, "/v1/apps/"+id+"/deployments", body)
	}

	// 1. Refusals, each naming its field; none saves anything.
	g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].ports[0].published", http.MethodPost,
		"/v1/apps/"+echoID+"/deployments", publishedEcho(22))
	two := strings.Replace(publishedEcho(publishedPort), `"name":"echo"`, `"name":"echo","replicas":2`, 1)
	g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].replicas", http.MethodPost,
		"/v1/apps/"+echoID+"/deployments", two)
	udpOnly := strings.Replace(publishedEcho(publishedPort), `"protocol":"tcp+udp"`, `"protocol":"udp"`, 1)
	g.refusal(t, http.StatusBadRequest, "invalid_field", "services[0].health", http.MethodPost,
		"/v1/apps/"+echoID+"/deployments", udpOnly)
	if _, _, err := g.target.Nomad.Jobs().Info(echoID, nil); err == nil {
		t.Fatalf("a refused deploy registered %s", echoID)
	}

	// 2. Deploy the echo app and a client beside it.
	var echo, client gateDeployment
	for id, want := range map[string]*gateDeployment{echoID: &echo, clientID: &client} {
		body := publishedEcho(publishedPort)
		if id == clientID {
			body = publishedClient
		}
		status, raw := post(id, body)
		if status != http.StatusAccepted || json.Unmarshal(raw, want) != nil || want.ID == "" {
			t.Fatalf("deploy %s = %d %s", id, status, raw)
		}
	}
	for id, d := range map[string]gateDeployment{echoID: echo, clientID: client} {
		if final := g.waitFinalOf(t, id, d.ID, 4*time.Minute); final.Status != "healthy" {
			t.Fatalf("deploy %s ended %s: %s", id, final.Status, final.Error)
		}
	}
	job, _, err := g.target.Nomad.Jobs().Info(echoID, nil)
	if err != nil {
		t.Fatalf("read the job %s: %v", echoID, err)
	}
	n := job.TaskGroups[0].Networks[0]
	if len(n.ReservedPorts) != 1 || n.ReservedPorts[0].Value != publishedPort || n.ReservedPorts[0].HostNetwork != "public" ||
		n.ReservedPorts[0].To != 4000 || len(n.DynamicPorts) != 0 {
		t.Fatalf("the job's network = %+v, want one static port %d on the host network public", n, publishedPort)
	}
	if u := job.TaskGroups[0].Update; u == nil || u.Canary == nil || *u.Canary != 0 {
		t.Errorf("the group's update = %+v, want no canary (stop, then start)", u)
	}

	// 3. The server's address, from the allocation's own record of where the port opened.
	addr := g.publishedAddress(t, echoID)
	probe := func(udp bool) string {
		cmd := "echo hi | nc -w 3 " + addr + " " + strconv.Itoa(publishedPort)
		if udp {
			cmd = "echo hi | nc -u -w 3 " + addr + " " + strconv.Itoa(publishedPort)
		}
		out, _ := g.exec(t, clientID, "client", client.ID, "sh", "-c", cmd)
		return strings.TrimSpace(out)
	}
	waitFor(t, "pong-tcp from "+addr, time.Minute, func() bool { return probe(false) == "pong-tcp" })
	waitFor(t, "pong-udp from "+addr, 30*time.Second, func() bool { return probe(true) == "pong-udp" })
	if g.target.Public != nil {
		// On a real host the answer also comes from here, through the host's firewall.
		g.dialPublished(t, g.target.Public.IP)
	}

	// A new version replaces the copy (stop, then start), and the port answers again: the old copy
	// must have let go of the fixed port for the new one to take it.
	redeploy := strings.Replace(publishedEcho(publishedPort), `"name":"echo"`, `"name":"echo","env":[{"name":"VERSION","value":"2"}]`, 1)
	status, raw := post(echoID, redeploy)
	var second gateDeployment
	if status != http.StatusAccepted || json.Unmarshal(raw, &second) != nil || second.ID == "" || second.ID == echo.ID {
		t.Fatalf("redeploy %s = %d %s", echoID, status, raw)
	}
	if final := g.waitFinalOf(t, echoID, second.ID, 4*time.Minute); final.Status != "healthy" {
		t.Fatalf("redeploy %s ended %s: %s", echoID, final.Status, final.Error)
	}
	waitFor(t, "pong-tcp from "+addr+" after the redeploy", time.Minute, func() bool { return probe(false) == "pong-tcp" })
	waitFor(t, "pong-udp from "+addr+" after the redeploy", 30*time.Second, func() bool { return probe(true) == "pong-udp" })

	// 4. The same number in another app.
	g.refusal(t, http.StatusConflict, "port_taken", "services[0].ports[0].published", http.MethodPost,
		"/v1/apps/"+otherID+"/deployments", publishedEcho(publishedPort))

	// 5. The export.
	g.exportMatchesLive(t, echoID)

	// 6. Delete: the port closes.
	resp, err := g.apiRequest(http.MethodDelete, "/v1/apps/"+echoID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	waitFor(t, "the port to close", 2*time.Minute, func() bool {
		return probe(false) == "" && probe(true) == ""
	})
	if g.target.Public != nil {
		if portOpen(g.target.Public.IP, publishedPort) {
			t.Errorf("%s:%d still answers after the app was deleted", g.target.Public.IP, publishedPort)
		}
	}
}

// publishedAddress is the address the app's running copy has its published port on, as Nomad
// recorded it when it placed the copy.
func (g *gate) publishedAddress(t *testing.T, appID string) string {
	t.Helper()
	var addr string
	waitFor(t, "a running copy of "+appID+" with its ports", time.Minute, func() bool {
		stubs, _, err := g.target.Nomad.Jobs().Allocations(appID, false, nil)
		if err != nil {
			return false
		}
		for _, s := range stubs {
			if s.ClientStatus != "running" {
				continue
			}
			alloc, _, err := g.target.Nomad.Allocations().Info(s.ID, nil)
			if err != nil || alloc.AllocatedResources == nil {
				continue
			}
			for _, p := range alloc.AllocatedResources.Shared.Ports {
				if p.Label == "turn" && p.Value == publishedPort && p.HostIP != "" {
					addr = p.HostIP
					return true
				}
			}
		}
		return false
	})
	return addr
}

// dialPublished calls host:publishedPort from this process over TCP and over UDP.
func (g *gate) dialPublished(t *testing.T, host string) {
	t.Helper()
	target := net.JoinHostPort(host, strconv.Itoa(publishedPort))
	waitFor(t, "pong-tcp from "+target+" through the firewall", time.Minute, func() bool {
		conn, err := net.DialTimeout("tcp", target, 3*time.Second)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = fmt.Fprint(conn, "hi\n")
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		return strings.TrimSpace(string(buf[:n])) == "pong-tcp"
	})
	waitFor(t, "pong-udp from "+target+" through the firewall", time.Minute, func() bool {
		conn, err := net.Dial("udp", target)
		if err != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = fmt.Fprint(conn, "hi\n")
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		return strings.TrimSpace(string(buf[:n])) == "pong-udp"
	})
}
