package api

import (
	"errors"
	"net/http"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// Published ports (spec 041): the two refusals only they give.
const (
	codePortTaken       = "port_taken"
	codeNoPublicNetwork = "no_public_network"
)

// portsError maps the published-port errors to their codes; mapError calls it.
func portsError(err error) *Error {
	var taken *service.PortTakenError
	var noNet *deploy.PublicNetworkError
	switch {
	case errors.As(err, &taken):
		return &Error{Status: http.StatusConflict, Code: codePortTaken, Field: taken.Field, Message: err.Error()}
	case errors.As(err, &noNet):
		return &Error{Status: http.StatusConflict, Code: codeNoPublicNetwork, Field: noNet.Field, Message: err.Error()}
	case errors.Is(err, deploy.ErrNoPublicNetwork):
		return &Error{Status: http.StatusConflict, Code: codeNoPublicNetwork, Message: err.Error()}
	}
	return nil
}
