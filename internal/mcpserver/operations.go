package mcpserver

import "net/http"

// The API operations the tools call: the ID in api/openapi.yaml, the method and
// the path. Nothing else of Caravana's API is reachable from here. A test
// checks each one against the contract, and another that every tool only
// makes calls the contract has.
var (
	opGetHealth             = operation{"getHealth", http.MethodGet, "/v1/health"}
	opListProjects          = operation{"listProjects", http.MethodGet, "/v1/projects"}
	opGetProject            = operation{"getProject", http.MethodGet, "/v1/projects/{project}"}
	opListApps              = operation{"listApps", http.MethodGet, "/v1/apps"}
	opGetApp                = operation{"getApp", http.MethodGet, "/v1/apps/{id}"}
	opListRecentDeployments = operation{"listRecentDeployments", http.MethodGet, "/v1/deployments"}
	opListDeployments       = operation{"listDeployments", http.MethodGet, "/v1/apps/{id}/deployments"}
	opGetDeployment         = operation{"getDeployment", http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}"}
	opListCopies            = operation{"listCopies", http.MethodGet, "/v1/apps/{id}/copies"}
	opGetLogs               = operation{"getLogs", http.MethodGet, "/v1/apps/{id}/logs"}
	opListVolumes           = operation{"listVolumes", http.MethodGet, "/v1/apps/{id}/volumes"}
	opListBackups           = operation{"listBackups", http.MethodGet, "/v1/apps/{id}/backups"}
	opListAppActions        = operation{"listAppActions", http.MethodGet, "/v1/apps/{id}/actions"}
	opListAttention         = operation{"listAttention", http.MethodGet, "/v1/attention"}
	opPreviewPromotion      = operation{"previewPromotion", http.MethodPost, "/v1/apps/{id}/promote/preview"}
	opExportApp             = operation{"exportApp", http.MethodGet, "/v1/apps/{id}/jobspec"}
	opExportDeployment      = operation{"exportDeployment", http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}/jobspec"}
	opGetCompose            = operation{"getCompose", http.MethodGet, "/v1/apps/{id}/compose"}
	opGetDeploymentCompose  = operation{"getDeploymentCompose", http.MethodGet, "/v1/apps/{id}/deployments/{deploymentId}/compose"}

	opDeployApp          = operation{"deployApp", http.MethodPost, "/v1/apps/{id}/deployments"}
	opRedeployDeployment = operation{"redeployDeployment", http.MethodPost, "/v1/apps/{id}/deployments/{deploymentId}/redeploy"}
	opPromoteApp         = operation{"promoteApp", http.MethodPost, "/v1/apps/{id}/promote"}
	opRestartApp         = operation{"restartApp", http.MethodPost, "/v1/apps/{id}/restart"}
	opStopApp            = operation{"stopApp", http.MethodPost, "/v1/apps/{id}/stop"}
	opStartApp           = operation{"startApp", http.MethodPost, "/v1/apps/{id}/start"}
	opCreateBackup       = operation{"createBackup", http.MethodPost, "/v1/apps/{id}/backups"}
)

// allOperations lists every operation above, for the contract test.
var allOperations = []operation{
	opGetHealth, opListProjects, opGetProject, opListApps, opGetApp,
	opListRecentDeployments, opListDeployments, opGetDeployment, opListCopies,
	opGetLogs, opListVolumes, opListBackups, opListAppActions, opListAttention,
	opPreviewPromotion, opExportApp, opExportDeployment, opGetCompose,
	opGetDeploymentCompose,
	opDeployApp, opRedeployDeployment, opPromoteApp, opRestartApp, opStopApp,
	opStartApp, opCreateBackup,
}
