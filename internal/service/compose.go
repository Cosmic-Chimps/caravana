package service

import (
	"context"
	"errors"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/compose"
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
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return DeployResult{}, err
	}
	file := req.WithoutSecretValues()
	res, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings}, nil)
	return res, inFile(err, a)
}

// inFile is compose.InFile, which names a refused field by its path in the
// file, for a DomainTakenError too.
func inFile(err error, a app.App) error {
	var taken *DomainTakenError
	if errors.As(err, &taken) {
		if fe, ok := compose.InFile(app.NewFieldError(taken.Field, ""), a).(*app.FieldError); ok {
			return &DomainTakenError{Field: fe.Field, Host: taken.Host, App: taken.App}
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
