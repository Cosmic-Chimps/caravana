package service

import (
	"context"
	"errors"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// DeployCompose turns a Compose file and its overlay into an app (package
// compose) and deploys it exactly as Deploy does, with the file's warnings
// in the deployment record. A file Caravana can't honor is an
// *app.FieldError naming the path in the file, and nothing is saved.
func (s *Service) DeployCompose(ctx context.Context, appID string, req compose.Request, trigger app.Trigger) (store.DeploymentRecord, error) {
	return s.DeployComposeIf(ctx, appID, req, Condition{}, trigger)
}

// DeployComposeIf is DeployCompose with a condition on the stored app, as
// DeployIf has: a new app only (IfNew) or an app still at the revision the
// caller saw (IfRevision).
//
// The request is kept with the app and the deployment, so the owner can edit
// the file later. The kept copy has no secret values; the request itself, with
// them, still feeds Translate, so the values reach the secret store.
func (s *Service) DeployComposeIf(ctx context.Context, appID string, req compose.Request, cond Condition, trigger app.Trigger) (store.DeploymentRecord, error) {
	return recordOnly(s.DeployComposeIfResult(ctx, appID, req, cond, trigger))
}

// DeployComposeIfResult is DeployComposeIf answering a DeployResult: with the
// password of each domain the deploy turned Team only, the one time it is
// shown (the API's deployCompose).
func (s *Service) DeployComposeIfResult(ctx context.Context, appID string, req compose.Request, cond Condition, trigger app.Trigger) (DeployResult, error) {
	return s.deployComposeWarned(ctx, appID, req, cond, trigger, nil)
}

// deployComposeWarned is DeployComposeIfResult with warnings of the caller's own
// added to the file's: what a template install tells about a choice the owner made.
func (s *Service) deployComposeWarned(ctx context.Context, appID string, req compose.Request, cond Condition, trigger app.Trigger, extra []app.Warning) (DeployResult, error) {
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return DeployResult{}, err
	}
	warnings = append(warnings, extra...)
	file := req.WithoutSecretValues()
	res, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings}, nil)
	return res, inFile(err, a, req)
}

// inFile is compose.InFile, which names a refused field by its path in the
// file (and a published port's by its entry in the request's overlay), for a
// DomainTakenError, a PortTakenError and a deploy.PublicNetworkError too.
func inFile(err error, a app.App, req compose.Request) error {
	rename := func(field string) (string, bool) {
		fe, ok := compose.InFile(app.NewFieldError(field, ""), a).(*app.FieldError)
		if !ok {
			return "", false
		}
		if renamed, ok := compose.PublishedInOverlay(fe, a, req).(*app.FieldError); ok {
			fe = renamed
		}
		return fe.Field, true
	}
	var domain *DomainTakenError
	var port *PortTakenError
	var network *deploy.PublicNetworkError
	switch {
	case errors.As(err, &domain):
		if f, ok := rename(domain.Field); ok {
			return &DomainTakenError{Field: f, Host: domain.Host, App: domain.App}
		}
	case errors.As(err, &port):
		if f, ok := rename(port.Field); ok {
			return &PortTakenError{Field: f, Port: port.Port, App: port.App}
		}
	case errors.As(err, &network):
		if f, ok := rename(network.Field); ok {
			return &deploy.PublicNetworkError{Field: f, Message: network.Message}
		}
	}
	return compose.InFile(err, a)
}

// PreviewCompose answers what DeployCompose would deploy: the app, with
// secret names only, and the file's warnings. It saves nothing and asks
// nobody: no registry, no Nomad, no secret store.
func (s *Service) PreviewCompose(ctx context.Context, appID string, req compose.Request) (app.App, []app.Warning, error) {
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return app.App{}, nil, err
	}
	a = a.WithLowercaseHosts()
	if err := s.deployer.Validate(a); err != nil {
		return app.App{}, nil, compose.InFile(err, a)
	}
	if err := s.checkConsoleDomain(a); err != nil {
		return app.App{}, nil, compose.InFile(err, a)
	}
	if warnings == nil {
		warnings = []app.Warning{}
	}
	return a.WithoutSecretValues(), warnings, nil
}
