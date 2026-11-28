// Package audit is the shape of an audit-log entry (feature 057): who did what, to what, and
// what changed. It is pure: no HTTP, no store, no service. The service builds an Entry after a
// change succeeded and the store keeps it; Diff turns a before and an after into field-level
// changes with secret material redacted, and CSV writes entries for export.
package audit

import "time"

// Door is how a request reached Caravana.
type Door string

// The doors. A request has one; Caravana's own work has caravana, the host's CLI has host.
const (
	DoorConsole  Door = "console"
	DoorAPI      Door = "api"
	DoorMCP      Door = "mcp"
	DoorWebhook  Door = "webhook"
	DoorCaravana Door = "caravana"
	DoorHost     Door = "host"
)

// Doors lists every door.
func Doors() []Door {
	return []Door{DoorConsole, DoorAPI, DoorMCP, DoorWebhook, DoorCaravana, DoorHost}
}

// ActorKind says what kind of thing acted.
type ActorKind string

// The actor kinds. A person is signed in; a token acts for its person; a deploy token is a token
// with a scope; a webhook acts for its app; caravana is Caravana itself (a reaper, an automatic
// update); host is the CLI on the server; anonymous is a sign-in that failed.
const (
	ActorPerson      ActorKind = "person"
	ActorToken       ActorKind = "token"
	ActorDeployToken ActorKind = "deploy_token"
	ActorWebhook     ActorKind = "webhook"
	ActorCaravana    ActorKind = "caravana"
	ActorHost        ActorKind = "host"
	ActorAnonymous   ActorKind = "anonymous"
)

// Actor is who did it. Email is the person's (or, for an anonymous sign-in failure, what was
// typed when it reads as an address); Token is the token's name; PersonID the person's ID. A
// webhook's actor has the app's ID as its Token.
type Actor struct {
	Kind     ActorKind `json:"kind"`
	Email    string    `json:"email,omitempty"`
	Token    string    `json:"token,omitempty"`
	PersonID string    `json:"personId,omitempty"`
}

// Action is a past-tense verb from a closed list: what was done.
type Action string

// The actions (plan D7). A note with another action is a programming error.
const (
	ActionCreated          Action = "created"
	ActionUpdated          Action = "updated"
	ActionDeleted          Action = "deleted"
	ActionDeployed         Action = "deployed"
	ActionRedeployed       Action = "redeployed"
	ActionRolledBack       Action = "rolled back"
	ActionPromoted         Action = "promoted"
	ActionCopied           Action = "copied"
	ActionStopped          Action = "stopped"
	ActionStarted          Action = "started"
	ActionRestarted        Action = "restarted"
	ActionRanCommand       Action = "ran a command"
	ActionOpenedTerminal   Action = "opened a terminal"
	ActionReadFile         Action = "read a file"
	ActionBackedUp         Action = "backed up"
	ActionRestored         Action = "restored"
	ActionPinned           Action = "pinned"
	ActionUnpinned         Action = "unpinned"
	ActionTested           Action = "tested"
	ActionRotated          Action = "rotated"
	ActionRevoked          Action = "revoked"
	ActionInvited          Action = "invited"
	ActionRemoved          Action = "removed"
	ActionAcceptedInvite   Action = "accepted an invite"
	ActionSignedIn         Action = "signed in"
	ActionSignedOut        Action = "signed out"
	ActionSignInFailed     Action = "sign-in failed"
	ActionRequested        Action = "requested"
	ActionCancelled        Action = "cancelled"
	ActionChecked          Action = "checked"
	ActionInstalled        Action = "installed"
	ActionUninstalled      Action = "uninstalled"
	ActionRefreshedSecrets Action = "refreshed secrets"
	// ActionChanged is the fallback of an API write nothing noted: the log still says someone changed
	// something there.
	ActionChanged Action = "changed"
)

// Actions lists every action, in the order of the constants.
func Actions() []Action {
	return []Action{
		ActionCreated, ActionUpdated, ActionDeleted, ActionDeployed, ActionRedeployed, ActionRolledBack,
		ActionPromoted, ActionCopied, ActionStopped, ActionStarted, ActionRestarted, ActionRanCommand,
		ActionOpenedTerminal, ActionReadFile, ActionBackedUp, ActionRestored, ActionPinned, ActionUnpinned, ActionTested,
		ActionRotated, ActionRevoked, ActionInvited, ActionRemoved, ActionAcceptedInvite, ActionSignedIn,
		ActionSignedOut, ActionSignInFailed, ActionRequested, ActionCancelled, ActionChecked,
		ActionInstalled, ActionUninstalled, ActionRefreshedSecrets, ActionChanged,
	}
}

// ValidAction reports whether a is in the closed list.
func ValidAction(a Action) bool {
	for _, k := range Actions() {
		if k == a {
			return true
		}
	}
	return false
}

// Kind is what kind of thing was acted on.
type Kind string

// The resource kinds (plan D8). A settings resource's ID says which settings: certificates,
// updates, templates, builds, cleanup, scans, previews, audit, sign-in. KindInstallation is the
// fallback of a write that names nothing narrower.
const (
	KindApp                 Kind = "app"
	KindProject             Kind = "project"
	KindEnvironment         Kind = "environment"
	KindSchedule            Kind = "schedule"
	KindBackup              Kind = "backup"
	KindDomain              Kind = "domain"
	KindToken               Kind = "token"
	KindWebhookToken        Kind = "webhook_token"
	KindRegistry            Kind = "registry"
	KindBackupDestination   Kind = "backup_destination"
	KindNotificationChannel Kind = "notification_channel"
	KindDNSZone             Kind = "dns_zone"
	KindSecretProvider      Kind = "secret_provider"
	KindPerson              Kind = "person"
	KindPasskey             Kind = "passkey"
	KindAccount             Kind = "account"
	KindServer              Kind = "server"
	KindVolume              Kind = "volume"
	KindAddon               Kind = "addon"
	KindGitAccess           Kind = "git_access"
	KindConsoleAddress      Kind = "console_address"
	KindCertificate         Kind = "certificate"
	KindSettings            Kind = "settings"
	KindInstallation        Kind = "installation"
)

// Kinds lists every resource kind.
func Kinds() []Kind {
	return []Kind{
		KindApp, KindProject, KindEnvironment, KindSchedule, KindBackup, KindDomain, KindToken,
		KindWebhookToken, KindRegistry, KindBackupDestination, KindNotificationChannel, KindDNSZone,
		KindSecretProvider, KindPerson, KindPasskey, KindAccount, KindServer, KindVolume, KindAddon,
		KindGitAccess, KindConsoleAddress, KindCertificate, KindSettings, KindInstallation,
	}
}

// ValidKind reports whether k is a resource kind.
func ValidKind(k Kind) bool {
	for _, v := range Kinds() {
		if v == k {
			return true
		}
	}
	return false
}

// Resource is what was acted on. Name is how people call it (an app's name, a host); Project and
// Environment place it when it belongs to one.
type Resource struct {
	Kind        Kind   `json:"kind"`
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Project     string `json:"project,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// RecordKind is the kind of record an entry links to (plan D9). The entry holds the kind and the
// ID as text, with no foreign key, so a record deleted with its app leaves the entry.
type RecordKind string

// The record kinds.
const (
	RecordDeployment           RecordKind = "deployment"
	RecordAppAction            RecordKind = "app_action"
	RecordBackup               RecordKind = "backup"
	RecordRestore              RecordKind = "restore"
	RecordScheduleRun          RecordKind = "schedule_run"
	RecordUpdateRun            RecordKind = "update_run"
	RecordCleanup              RecordKind = "cleanup"
	RecordVolumeDeletion       RecordKind = "volume_deletion"
	RecordConsoleAddressChange RecordKind = "console_address_change"
	RecordCertificateDrop      RecordKind = "certificate_drop"
	RecordSelfBackup           RecordKind = "self_backup"
	RecordSelfRestore          RecordKind = "self_restore"
)

// Record links an entry to the record it is about.
type Record struct {
	Kind RecordKind `json:"kind"`
	ID   string     `json:"id"`
}

// Related is another resource the same request touched, with what happened to it.
type Related struct {
	Kind   Kind   `json:"kind"`
	ID     string `json:"id,omitempty"`
	Action Action `json:"action"`
}

// Entry is what the service notes after a change: everything but the entry's ID and time, which
// the store gives it. Operation is the route, "METHOD /pattern" (or a console form's
// "POST /pattern"); Address is the client's, when there was a request.
type Entry struct {
	Actor     Actor     `json:"actor"`
	Door      Door      `json:"door"`
	Address   string    `json:"address,omitempty"`
	Action    Action    `json:"action"`
	Operation string    `json:"operation,omitempty"`
	Resource  Resource  `json:"resource"`
	Changes   []Change  `json:"changes"`
	Record    *Record   `json:"record,omitempty"`
	Related   []Related `json:"related,omitempty"`
	Fallback  bool      `json:"fallback"`
}

// Event is a stored entry: the entry with the ID that orders the log and the time it was written.
type Event struct {
	ID int64     `json:"id"`
	At time.Time `json:"at"`
	Entry
}
