package app

// Trigger says what started a deployment.
type Trigger string

// The triggers Caravana knows: the API, a webhook, and the web console.
const (
	TriggerManual  Trigger = "manual"
	TriggerWebhook Trigger = "webhook"
	// TriggerConsole is a deploy started from the web console.
	TriggerConsole Trigger = "console"
)

// Deployment is one go-live event for an App. Image tags are resolved to
// digests before translation, so Nomad always sees an exact image and every
// deploy is a new, revertable job version.
//
// Status, timing and the Nomad job version belong to the deploy flow, which
// records them; they are not part of the translator's input.
type Deployment struct {
	// ID is unique per deploy. It goes into the job's meta, which makes every
	// deploy a new job version even when nothing else changed.
	ID string `json:"id"`
	// Trigger is TriggerManual, TriggerWebhook or TriggerConsole.
	Trigger Trigger `json:"trigger"`
	// Digests maps each service name to its resolved image, in the form
	// "repo@sha256:<64 hex>". Exactly one entry per service.
	Digests map[string]string `json:"digests"`
	// Origin says where the deploy came from (a CI run); nil when nobody said.
	// The translator ignores it: it is recorded with the deployment only.
	Origin *Origin `json:"origin,omitempty"`
}
