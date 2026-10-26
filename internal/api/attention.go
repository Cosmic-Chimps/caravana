package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// attentionItemJSON is one thing that needs the owner (schema AttentionItem).
// It carries the facts; the console writes the sentence.
type attentionItemJSON struct {
	Kind             string   `json:"kind"`
	AppID            string   `json:"appId,omitempty"`
	Project          string   `json:"project,omitempty"`
	Environment      string   `json:"environment,omitempty"`
	AppName          string   `json:"appName,omitempty"`
	Service          string   `json:"service,omitempty"`
	Schedule         string   `json:"schedule,omitempty"`   // the schedule kinds (feature 022): its name
	ScheduleID       string   `json:"scheduleId,omitempty"` // and its ID
	Copies           []string `json:"copies,omitempty"`
	DeploymentID     string   `json:"deploymentId,omitempty"`
	DeploymentStatus string   `json:"deploymentStatus,omitempty"`
	JobVersion       *uint64  `json:"jobVersion,omitempty"`
	// The domain kinds (feature 023): the host, the address its DNS gave that
	// isn't this host's, and when its certificate ends.
	Domain      string     `json:"domain,omitempty"`
	AddressSeen string     `json:"addressSeen,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	// disk_nearly_full (feature 025): the filesystem, what is on it, how full
	// it is, what is left and the level. Host-level, so there is no appId.
	Mount       string    `json:"mount,omitempty"`
	Holds       []string  `json:"holds,omitempty"`
	UsedPercent *float64  `json:"usedPercent,omitempty"`
	FreeBytes   *int64    `json:"freeBytes,omitempty"`
	Level       string    `json:"level,omitempty"`
	At          time.Time `json:"at"`
	Detail      string    `json:"detail,omitempty"`
}

func toAttentionItemJSON(it service.AttentionItem) attentionItemJSON {
	out := attentionItemJSON{Kind: string(it.Kind), AppID: it.AppID, Project: it.Project, Environment: it.Environment, AppName: it.AppName,
		Service: it.Service, Schedule: it.Schedule, ScheduleID: it.ScheduleID, Copies: it.Copies,
		DeploymentID: it.Deployment, DeploymentStatus: string(it.DeploymentStatus), JobVersion: it.Version,
		Domain: it.Domain, AddressSeen: it.AddressSeen, ExpiresAt: it.ExpiresAt, At: it.When, Detail: it.Detail}
	if it.Kind == service.AttentionDiskNearlyFull {
		out.Mount, out.Holds, out.Level = it.Mount, it.Holds, string(it.Level)
		out.UsedPercent, out.FreeBytes = &it.UsedPercent, &it.FreeBytes
	}
	return out
}

// listAttention answers GET /v1/attention: what needs the owner, newest
// first, the list the console's home shows. When Nomad can't be read the
// answer is still 200: the items that come from the store are there, and
// copiesUnknown says the copy ones may be missing.
func (s *server) listAttention(w http.ResponseWriter, r *http.Request) error {
	att, err := s.svc.Attention(r.Context())
	if err != nil {
		return err
	}
	out := struct {
		Items         []attentionItemJSON `json:"items"`
		CopiesUnknown bool                `json:"copiesUnknown"`
		DisksUnknown  bool                `json:"disksUnknown"`
	}{Items: make([]attentionItemJSON, 0, len(att.Items)), CopiesUnknown: att.CopiesUnknown, DisksUnknown: att.DisksUnknown}
	for _, it := range att.Items {
		out.Items = append(out.Items, toAttentionItemJSON(it))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
