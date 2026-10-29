package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The action tools send no Caravana-Origin-* header on purpose: a deploy the
// agent starts is not from CI, and an origin with only an actor would read
// "From CI: by mcp" in notifications. Caravana records it as started by the
// API, like any token's.

// addActionTools registers the tools that change things. They are offered only
// with --allow-actions; without it they don't exist, so calling one by name
// fails like any unknown tool.
func (h *handler) addActionTools(srv *mcp.Server) {
	act := func(name, description string, destructive bool) toolSpec {
		return toolSpec{name: name, description: description, action: true, destructive: destructive}
	}

	addTool(h, srv, act("redeploy_app",
		"Deploys the app again as it is stored: every image tag is resolved again, so a moving tag such as "+
			"latest picks up a new image. Changes a running app. Answers the new deployment; follow it with "+
			"wait_for_deployment. API operation deployApp with an empty body.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in appInput) (string, error) {
			return h.answer(ctx, call{op: opDeployApp, params: []string{"id", in.App}})
		})

	addTool(h, srv, act("deploy_image",
		"Deploys a new image to one service of an app and keeps everything else in the stored app. Reads the "+
			"app (getApp), replaces only that service's image, and deploys it (deployApp) only if nobody "+
			"changed the app meanwhile (If-Match with the revision it read; retried once if the app changed). "+
			"`service` is needed when the app has more than one. An app deployed from a Compose file loses "+
			"its kept file (the answer's warnings say so). Changes a running app. Answers the new deployment; "+
			"follow it with wait_for_deployment.", true),
		h.deployImage)

	addTool(h, srv, act("redeploy_version",
		"Deploys a past healthy deployment of the app again (a rollback), with the images it pinned. Changes a "+
			"running app. Answers the new deployment; follow it with wait_for_deployment. API operation "+
			"redeployDeployment.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in deploymentInput) (string, error) {
			return h.answer(ctx, call{op: opRedeployDeployment, params: []string{"id", in.App, "deploymentId", in.Deployment}})
		})

	addTool(h, srv, act("promote_app",
		"Promotes the app's newest healthy deployment into another environment of its project (for example "+
			"staging to production), where the same app must already exist. Previews it first "+
			"(previewPromotion), then promotes (promoteApp) only if the target is still as the preview saw "+
			"it. Changes a running app in another environment. Answers the target's new deployment; follow it "+
			"with wait_for_deployment using the target app's ID.", true),
		h.promoteApp)

	addTool(h, srv, act("restart_app",
		"Restarts the app's copies one at a time. Each copy is down while it restarts, so the app may be "+
			"unavailable for a while if it has one copy. Answers at once; the restart goes on in the "+
			"background (see list_app_actions). API operation restartApp.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in appInput) (string, error) {
			return h.answer(ctx, call{op: opRestartApp, params: []string{"id", in.App}})
		})

	addTool(h, srv, act("stop_app",
		"Stops the app: it serves nothing until start_app, and deploys are refused meanwhile. Its data and "+
			"settings are kept. API operation stopApp.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in appInput) (string, error) {
			return h.answer(ctx, call{op: opStopApp, params: []string{"id", in.App}})
		})

	addTool(h, srv, act("start_app",
		"Starts a stopped app again, with the same job and images it had. API operation startApp.", false),
		func(ctx context.Context, _ *mcp.CallToolRequest, in appInput) (string, error) {
			return h.answer(ctx, call{op: opStartApp, params: []string{"id", in.App}})
		})

	addTool(h, srv, act("back_up_now",
		"Backs up one service that has a backup policy now. A backup of a volume stops the service while it "+
			"runs and starts it again. Runs in the background; list_backups shows it. API operation "+
			"createBackup.", true),
		func(ctx context.Context, _ *mcp.CallToolRequest, in backupInput) (string, error) {
			return h.answer(ctx, call{op: opCreateBackup, params: []string{"id", in.App}, body: map[string]string{"service": in.Service}})
		})
}

// ifMatchHeader is the If-Match header for a revision: one quoted value, as
// getApp's ETag has it (the API refuses an unquoted one).
func ifMatchHeader(revision string) map[string]string {
	return map[string]string{"If-Match": `"` + revision + `"`}
}

// isAppChanged says whether err is the API's 412: the app changed since the
// revision the call carried.
func isAppChanged(err error) bool {
	var te *toolError
	return errors.As(err, &te) && te.Status == http.StatusPreconditionFailed && te.Code == "app_changed"
}

// promoteApp previews the promotion and promotes with the target's revision
// from the preview, so a target that changed in between is refused (412) and
// not overwritten.
func (h *handler) promoteApp(ctx context.Context, _ *mcp.CallToolRequest, in promotionInput) (string, error) {
	body := map[string]string{"to": in.To}
	r, err := h.api.do(ctx, call{op: opPreviewPromotion, params: []string{"id", in.App}, body: body})
	if err != nil {
		return "", err
	}
	var preview struct {
		TargetRevision string `json:"targetRevision"`
	}
	if err := json.Unmarshal(r.body, &preview); err != nil || preview.TargetRevision == "" {
		return "", &toolError{Code: "bad_answer", Text: "Caravana's promotion preview had no targetRevision"}
	}

	text, err := h.answer(ctx, call{op: opPromoteApp, params: []string{"id", in.App}, body: body,
		header: ifMatchHeader(preview.TargetRevision)})
	if isAppChanged(err) {
		var te *toolError
		errors.As(err, &te)
		return "", &toolError{Code: te.Code, Status: te.Status,
			Text: te.Text + ": the target changed after the preview; call preview_promotion and promote again"}
	}
	return text, err
}

// deployImage replaces one service's image in the stored app and deploys it
// with the revision it read. If the app changed in between (412), it reads
// again and tries once more.
func (h *handler) deployImage(ctx context.Context, _ *mcp.CallToolRequest, in deployImageInput) (string, error) {
	text, err := h.deployImageOnce(ctx, in)
	if isAppChanged(err) {
		return h.deployImageOnce(ctx, in)
	}
	return text, err
}

func (h *handler) deployImageOnce(ctx context.Context, in deployImageInput) (string, error) {
	r, err := h.api.do(ctx, call{op: opGetApp, params: []string{"id", in.App}})
	if err != nil {
		return "", err
	}
	definition, revision, err := withImage(r.body, in.App, in.Service, in.Image)
	if err != nil {
		return "", err
	}
	return h.answer(ctx, call{op: opDeployApp, params: []string{"id", in.App}, raw: definition,
		header: ifMatchHeader(revision)})
}

// withImage takes the answer of getApp and returns the app's definition (the
// body deployApp takes) with the image of one service replaced, and the
// revision that was read. Everything else is kept as the API sent it: the JSON
// is handled as raw members, so no field is dropped or reinterpreted, whatever
// this binary knows of them.
func withImage(record []byte, appID, service, image string) (definition []byte, revision string, err error) {
	var rec struct {
		App      json.RawMessage `json:"app"`
		Revision string          `json:"revision"`
	}
	if err := json.Unmarshal(record, &rec); err != nil || len(rec.App) == 0 || rec.Revision == "" {
		return nil, "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the app had no app or revision"}
	}
	var def map[string]json.RawMessage
	var services []json.RawMessage
	if err := json.Unmarshal(rec.App, &def); err != nil {
		return nil, "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the app was not an app"}
	}
	if err := json.Unmarshal(def["services"], &services); err != nil && def["services"] != nil {
		return nil, "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the app had no list of services"}
	}

	names := make([]string, len(services))
	members := make([]map[string]json.RawMessage, len(services))
	for i, s := range services {
		if err := json.Unmarshal(s, &members[i]); err != nil {
			return nil, "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the app held a service that was not an object"}
		}
		if err := json.Unmarshal(members[i]["name"], &names[i]); err != nil {
			return nil, "", &toolError{Code: "bad_answer", Text: "Caravana's answer for the app held a service with no name"}
		}
	}

	idx, err := pickService(appID, service, names)
	if err != nil {
		return nil, "", err
	}
	if members[idx]["image"], err = json.Marshal(image); err != nil {
		return nil, "", fmt.Errorf("encode the image: %w", err)
	}
	if services[idx], err = json.Marshal(members[idx]); err != nil {
		return nil, "", fmt.Errorf("encode the service: %w", err)
	}
	if def["services"], err = json.Marshal(services); err != nil {
		return nil, "", fmt.Errorf("encode the services: %w", err)
	}
	if definition, err = json.Marshal(def); err != nil {
		return nil, "", fmt.Errorf("encode the app: %w", err)
	}
	return definition, rec.Revision, nil
}

// pickService says which service to change: the named one, or the only one.
func pickService(appID, service string, names []string) (int, error) {
	list := strings.Join(names, ", ")
	switch {
	case len(names) == 0:
		return 0, invalidInput("app %s has no services yet: define it first (in the console's Configure), then deploy", appID)
	case service == "" && len(names) > 1:
		return 0, invalidInput("app %s has %d services (%s): say which one with `service`", appID, len(names), list)
	case service == "":
		return 0, nil
	}
	for i, n := range names {
		if n == service {
			return i, nil
		}
	}
	return 0, invalidInput("app %s has no service %q; its services are: %s", appID, service, list)
}
