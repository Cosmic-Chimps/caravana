package service

import (
	"context"

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
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	file := req.WithoutSecretValues()
	rec, err := s.saveAndDeploy(ctx, a, cond, trigger, made{file: &file, warnings: warnings}, nil)
	return rec, compose.InFile(err, a)
}

// PreviewCompose answers what DeployCompose would deploy: the app, with
// secret names only, and the file's warnings. It saves nothing and asks
// nobody: no registry, no Nomad, no secret store.
func (s *Service) PreviewCompose(ctx context.Context, appID string, req compose.Request) (app.App, []app.Warning, error) {
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return app.App{}, nil, err
	}
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
