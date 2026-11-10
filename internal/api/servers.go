package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Servers (feature 031): the console calls of specs/031-add-a-server/contracts/api.md §5. They are
// authenticated like every other route (a session or a full API token; a deploy token is refused by
// the scope middleware's default deny). The node calls, which carry their own credentials, are in
// nodecalls.go.

const (
	codeClusterNotReady  = "cluster_not_ready"
	codeClusterFull      = "cluster_full"
	codeServerExists     = "server_exists"
	codeServerNotFound   = "server_not_found"
	codeServerRunning    = "server_running"
	codeJoinTokenInvalid = "join_token_invalid"
	codeNodeTokenInvalid = "node_token_invalid"
	codeTooManyAttempts  = "too_many_attempts"
)

// serversError maps the errors of the servers calls. It returns nil for any other error.
func serversError(err error) *Error {
	switch {
	case errors.Is(err, service.ErrClusterNotReady):
		return &Error{Status: http.StatusConflict, Code: codeClusterNotReady, Message: err.Error()}
	case errors.Is(err, service.ErrClusterFull):
		return &Error{Status: http.StatusConflict, Code: codeClusterFull, Message: "The cluster has the most servers it can have."}
	case errors.Is(err, service.ErrServerExists):
		field := "name"
		var re *service.RefusalError
		if errors.As(err, &re) {
			field = re.Field
		}
		return &Error{Status: http.StatusConflict, Code: codeServerExists, Field: field, Message: "A server or a Nomad node already has that name."}
	case errors.Is(err, service.ErrServerNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeServerNotFound, Message: err.Error()}
	case errors.Is(err, service.ErrServerRunning):
		return &Error{Status: http.StatusConflict, Code: codeServerRunning, Message: err.Error()}
	case errors.Is(err, service.ErrFirstServer):
		return &Error{Status: http.StatusConflict, Code: codeProtected, Message: err.Error()}
	case errors.Is(err, service.ErrJoinToken):
		return &Error{Status: http.StatusUnauthorized, Code: codeJoinTokenInvalid, Message: err.Error()}
	case errors.Is(err, service.ErrNodeToken):
		return &Error{Status: http.StatusUnauthorized, Code: codeNodeTokenInvalid, Message: err.Error(), Authenticate: "Bearer"}
	}
	return nil
}

// unprocessable turns a field error of a request body into 422 invalid_field, which is what the
// servers calls answer for a bad value (the rest of the API answers 400). Any other error passes.
func unprocessable(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	}
	return err
}

// timeText is an RFC 3339 time, or "" for none: the servers' answers write "not yet" as an empty
// string.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

type serverCheckJSON struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func toServerChecksJSON(cs []service.ServerCheck) []serverCheckJSON {
	out := make([]serverCheckJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, serverCheckJSON{Name: c.Name, OK: c.OK, Detail: c.Detail})
	}
	return out
}

type serverAgentJSON struct {
	Caravana string `json:"caravana"`
	Nomad    string `json:"nomad"`
	Docker   string `json:"docker"`
	Kernel   string `json:"kernel"`
}

type serverMemoryJSON struct {
	Used     int64 `json:"used"`
	Reserved int64 `json:"reserved"`
	Total    int64 `json:"total"`
}

type serverCPUJSON struct {
	Used     int64 `json:"used"`
	Reserved int64 `json:"reserved"`
	Total    int64 `json:"total"`
	Cores    int64 `json:"cores"`
}

// serverJSON is schema Server. Memory, CPU and disks are null for a server with no sample yet,
// never 0.
type serverJSON struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Index                int               `json:"index"`
	Pool                 string            `json:"pool"`
	State                string            `json:"state"`
	Managed              bool              `json:"managed"`
	First                bool              `json:"first"`
	Ingress              bool              `json:"ingress"`
	ControlPlane         bool              `json:"controlPlane"`
	NomadServer          bool              `json:"nomadServer"`
	AcceptsWork          bool              `json:"acceptsWork"`
	Address              string            `json:"address"`
	PrivateAddress       string            `json:"privateAddress"`
	CreatedAt            string            `json:"createdAt"`
	JoinedAt             string            `json:"joinedAt"`
	ReadyAt              string            `json:"readyAt"`
	SeenAt               string            `json:"seenAt"`
	ExpiresAt            string            `json:"expiresAt"`
	Checks               []serverCheckJSON `json:"checks"`
	Agent                *serverAgentJSON  `json:"agent"`
	CertificateExpiresAt string            `json:"certificateExpiresAt"`
	Memory               *serverMemoryJSON `json:"memory"`
	CPU                  *serverCPUJSON    `json:"cpu"`
	Disks                *[]hostDiskJSON   `json:"disks"`
	Copies               int               `json:"copies"`
	Unreachable          []string          `json:"unreachable"`
}

func toServerJSON(v service.ServerView) serverJSON {
	out := serverJSON{
		ID: v.ID, Name: v.Name, Index: v.Index, Pool: v.Pool, State: v.State, Managed: v.Managed,
		First: v.First, Ingress: v.Ingress, ControlPlane: v.ControlPlane, NomadServer: v.NomadServer, AcceptsWork: v.AcceptsWork,
		Address: v.Address, PrivateAddress: v.PrivateAddress,
		CreatedAt: timeText(v.CreatedAt), JoinedAt: timeText(v.JoinedAt), ReadyAt: timeText(v.ReadyAt),
		SeenAt: timeText(v.SeenAt), ExpiresAt: timeText(v.ExpiresAt),
		Checks: toServerChecksJSON(v.Checks), CertificateExpiresAt: timeText(v.CertificateExpiresAt),
		Copies: v.Copies, Unreachable: v.Unreachable,
	}
	if out.Unreachable == nil {
		out.Unreachable = []string{}
	}
	if v.Agent != nil {
		out.Agent = &serverAgentJSON{Caravana: v.Agent.Caravana, Nomad: v.Agent.Nomad, Docker: v.Agent.Docker, Kernel: v.Agent.Kernel}
	}
	if v.Memory != nil {
		out.Memory = &serverMemoryJSON{Used: v.Memory.Used, Reserved: v.Memory.Reserved, Total: v.Memory.Total}
	}
	if v.CPU != nil {
		out.CPU = &serverCPUJSON{Used: v.CPU.Used, Reserved: v.CPU.Reserved, Total: v.CPU.Total, Cores: v.CPU.Cores}
	}
	if v.Disks != nil {
		disks := make([]hostDiskJSON, 0, len(v.Disks))
		for _, d := range v.Disks {
			holds := d.Holds
			if holds == nil {
				holds = []string{}
			}
			disks = append(disks, hostDiskJSON{Mount: d.Mount, Holds: holds, UsedMB: d.UsedMB, TotalMB: d.TotalMB, FreeMB: d.FreeMB,
				UsedPercent: d.UsedPercent, State: string(d.State)})
		}
		out.Disks = &disks
	}
	return out
}

// clusterJSON is schema Cluster.
type clusterJSON struct {
	Servers         int    `json:"servers"`
	Ready           int    `json:"ready"`
	NomadServers    int    `json:"nomadServers"`
	SurvivesLoss    bool   `json:"survivesLoss"`
	MemoryMB        int64  `json:"memoryMb"`
	CPUCores        int64  `json:"cpuCores"`
	CanAdd          bool   `json:"canAdd"`
	CannotAddReason string `json:"cannotAddReason"`
	Nomad           string `json:"nomad"`
}

func toClusterJSON(c service.ClusterView) clusterJSON {
	return clusterJSON{Servers: c.Servers, Ready: c.Ready, NomadServers: c.NomadServers, SurvivesLoss: c.SurvivesLoss,
		MemoryMB: c.MemoryMB, CPUCores: c.CPUCores, CanAdd: c.CanAdd, CannotAddReason: c.CannotAddReason, Nomad: c.Nomad}
}

// listServers answers GET /v1/servers: the cluster and every server, the ones Caravana manages and
// the Nomad nodes it doesn't.
func (s *server) listServers(w http.ResponseWriter, r *http.Request) error {
	view, err := s.svc.ListServers(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Cluster clusterJSON  `json:"cluster"`
		Servers []serverJSON `json:"servers"`
	}{Cluster: toClusterJSON(view.Cluster), Servers: make([]serverJSON, 0, len(view.Servers))}
	for _, v := range view.Servers {
		out.Servers = append(out.Servers, toServerJSON(v))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
	return nil
}

// getServer answers GET /v1/servers/{name}.
func (s *server) getServer(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.Server(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toServerJSON(v))
	return nil
}

// getServerJoin answers GET /v1/servers/joins/{id}: a server by the ID of its join, which exists
// before its name does.
func (s *server) getServerJoin(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.ServerByID(r.Context(), r.PathValue("id"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toServerJSON(v))
	return nil
}

// addServer answers POST /v1/servers with 201: a waiting server and its join command. The token is
// in this answer and nowhere else, so it is never cached.
func (s *server) addServer(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name string `json:"name"`
		Pool string `json:"pool"`
	}
	if _, err := decodeOptionalJSON(r, &in); err != nil {
		return err
	}
	scheme := "http"
	if s.proxies.HTTPS(r) {
		scheme = "https"
	}
	join, err := s.svc.CreateJoin(r.Context(), in.Name, in.Pool, scheme+"://"+r.Host)
	if err != nil {
		return unprocessable(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, struct {
		Server      serverJSON `json:"server"`
		Token       string     `json:"token"`
		Command     string     `json:"command"`
		CommandNote string     `json:"commandNote"`
		ExpiresAt   string     `json:"expiresAt"`
	}{toServerJSON(join.Server), join.Token, join.Command, join.CommandNote, timeText(join.ExpiresAt)})
	return nil
}

// removeServer answers DELETE /v1/servers/{name} with 204; the name may be a server's ID, which is how a
// waiting join is cancelled.
func (s *server) removeServer(w http.ResponseWriter, r *http.Request) error {
	if err := s.svc.RemoveServer(r.Context(), r.PathValue("name")); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
