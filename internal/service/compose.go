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
	a, warnings, err := compose.Translate(appID, req)
	if err != nil {
		return store.DeploymentRecord{}, err
	}
	return s.saveAndDeploy(ctx, a, trigger, warnings)
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
		return app.App{}, nil, err
	}
	if warnings == nil {
		warnings = []app.Warning{}
	}
	return a.WithoutSecretValues(), warnings, nil
}
