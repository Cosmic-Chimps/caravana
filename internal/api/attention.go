package api

import (
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/service"
)

// attentionItemJSON is one thing that needs the owner (schema AttentionItem).
// It carries the facts; the console writes the sentence.
type attentionItemJSON struct {
	Kind             string    `json:"kind"`
	AppID            string    `json:"appId,omitempty"`
	Project          string    `json:"project,omitempty"`
	Environment      string    `json:"environment,omitempty"`
	AppName          string    `json:"appName,omitempty"`
	Service          string    `json:"service,omitempty"`
	Copies           []string  `json:"copies,omitempty"`
	DeploymentID     string    `json:"deploymentId,omitempty"`
	DeploymentStatus string    `json:"deploymentStatus,omitempty"`
	JobVersion       *uint64   `json:"jobVersion,omitempty"`
	At               time.Time `json:"at"`
	Detail           string    `json:"detail,omitempty"`
}

func toAttentionItemJSON(it service.AttentionItem) attentionItemJSON {
	return attentionItemJSON{Kind: string(it.Kind), AppID: it.AppID, Project: it.Project, Environment: it.Environment, AppName: it.AppName,
		Service: it.Service, Copies: it.Copies,
		DeploymentID: it.Deployment, DeploymentStatus: string(it.DeploymentStatus), JobVersion: it.Version, At: it.When, Detail: it.Detail}
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
	}{Items: make([]attentionItemJSON, 0, len(att.Items)), CopiesUnknown: att.CopiesUnknown}
	for _, it := range att.Items {
		out.Items = append(out.Items, toAttentionItemJSON(it))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
