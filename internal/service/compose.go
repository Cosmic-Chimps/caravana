package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// errNotImagesOnly says a deploy is not the change keptFileForImages handles: nothing but
// images differs from the stored app, and at least one does. It is no failure worth a log line.
var errNotImagesOnly = errors.New("not an images-only change")

// keptFileForImages is how an images-only deploy of an app that keeps its Compose file keeps
// it: the kept file with the `image:` of each changed service rewritten in place
// (compose.ReplaceImages), the request that carries it (no secret value) and what
// translating it warns. An error says why it can't be done, and the caller then drops the
// file as any deploy from a definition does.
//
// The guard is the translation: the new file must make exactly the app being deployed
// (compared as the JSON the store keeps, hosts in lower case, secret values left out), so a
// file that doesn't say what the app says (a variable image, an alias, a preview's Team only
// domains) is never kept as if it did.
func keptFileForImages(old store.AppRecord, a app.App) (compose.Request, []app.Warning, error) {
	if old.Compose == nil {
		return compose.Request{}, nil, errors.New("the app keeps no file")
	}
	if _, ok := app.ImagesOnlyChange(old.App, a); !ok {
		return compose.Request{}, nil, errNotImagesOnly
	}
	changed := map[string]string{}
	for i, svc := range a.Services {
		if svc.Image != old.App.Services[i].Image {
			changed[svc.Name] = svc.Image
		}
	}
	if len(changed) == 0 {
		return compose.Request{}, nil, errNotImagesOnly
	}
	text, err := compose.ReplaceImages(old.Compose.Compose, changed)
	if err != nil {
		return compose.Request{}, nil, err
	}
	req := *old.Compose
	req.Compose = text
	translated, warnings, err := compose.Translate(a.ID, req)
	if err != nil {
		return compose.Request{}, nil, fmt.Errorf("the file with the new images does not translate: %w", err)
	}
	got, err1 := json.Marshal(translated.WithLowercaseHosts().WithoutSecretValues())
	want, err2 := json.Marshal(a.WithoutSecretValues())
	if err1 != nil || err2 != nil || string(got) != string(want) {
		return compose.Request{}, nil, errors.New("the file with the new images does not make the app being deployed")
	}
	return req.WithoutSecretValues(), warnings, nil
}
