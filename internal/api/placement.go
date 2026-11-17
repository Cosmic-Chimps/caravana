package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Placement and drain (feature 042): the codes only they give, and the three server calls.
const (
	codePlacementImpossible = "placement_impossible"
	codeServerMoving        = "server_moving"
	codeServerNotReady      = "server_not_ready"
	codeNoOtherServer       = "no_other_server"
)

// placementError maps a deploy refused for its placement rules, beside no_public_network; mapError
// calls it. The field is the rule's.
func placementError(err error) *Error {
	var pe *deploy.PlacementError
	switch {
	case errors.As(err, &pe):
		return &Error{Status: http.StatusConflict, Code: codePlacementImpossible, Field: pe.Field, Message: err.Error()}
	case errors.Is(err, deploy.ErrPlacementImpossible):
		return &Error{Status: http.StatusConflict, Code: codePlacementImpossible, Message: err.Error()}
	}
	return nil
}

// serverMoveJSON is schema ServerMove.
type serverMoveJSON struct {
	ID         string               `json:"id"`
	State      string               `json:"state"`
	StartedAt  string               `json:"startedAt"`
	DeadlineAt string               `json:"deadlineAt"`
	EndedAt    string               `json:"endedAt"`
	Moved      int                  `json:"moved"`
	Left       int                  `json:"left"`
	Stays      []serverMoveStayJSON `json:"stays"`
	Error      string               `json:"error"`
}

type serverMoveStayJSON struct {
	App     string `json:"app"`
	Service string `json:"service"`
	Reason  string `json:"reason"`
}

func toServerMoveJSON(m *service.MoveView) *serverMoveJSON {
	if m == nil {
		return nil
	}
	out := &serverMoveJSON{ID: m.ID, State: m.State, StartedAt: timeText(m.StartedAt), DeadlineAt: timeText(m.DeadlineAt),
		EndedAt: timeText(m.EndedAt), Moved: m.Moved, Left: m.Left, Error: m.Error, Stays: make([]serverMoveStayJSON, 0, len(m.Stays))}
	for _, st := range m.Stays {
		out.Stays = append(out.Stays, serverMoveStayJSON{App: st.App, Service: st.Service, Reason: st.Reason})
	}
	return out
}

// putServerLabels answers PUT /v1/servers/{name}/labels with the server.
func (s *server) putServerLabels(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Labels map[string]string `json:"labels"`
	}
	if err := decodeJSON(r, &in); err != nil {
		return err
	}
	v, err := s.svc.SetServerLabels(r.Context(), r.PathValue("name"), in.Labels)
	if err != nil {
		return unprocessable(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toServerJSON(v))
	return nil
}

// drainServer answers POST /v1/servers/{name}/drain with 202 and the server: the move is going in the
// background and the server's move says how.
func (s *server) drainServer(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		DeadlineSeconds int `json:"deadlineSeconds"`
	}
	if _, err := decodeOptionalJSON(r, &in); err != nil {
		return err
	}
	v, err := s.svc.MoveWorkOff(r.Context(), r.PathValue("name"), time.Duration(in.DeadlineSeconds)*time.Second)
	if err != nil {
		return unprocessable(err)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, toServerJSON(v))
	return nil
}

// undrainServer answers DELETE /v1/servers/{name}/drain with the server.
func (s *server) undrainServer(w http.ResponseWriter, r *http.Request) error {
	v, err := s.svc.AcceptWork(r.Context(), r.PathValue("name"))
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, toServerJSON(v))
	return nil
}
