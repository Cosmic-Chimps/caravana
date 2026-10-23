// Package notify turns one event ("a backup failed", "a deploy ended") into
// the HTTP request a notification channel expects, and sends it (feature 018).
// It knows four kinds of channel: a signed JSON webhook, a Slack incoming
// webhook, a Discord webhook and an ntfy topic.
//
// The package depends on no other package of Caravana: callers hand it an
// Event and a Channel and get a request back. Render is pure, so what each kind
// sends is pinned by golden files; Send is the only function that touches the
// network.
package notify

import "time"

// The kinds of channel.
const (
	KindWebhook = "webhook"
	KindSlack   = "slack"
	KindDiscord = "discord"
	KindNtfy    = "ntfy"
)

// Kinds lists the kinds of channel in the order the console offers them.
var Kinds = []string{KindNtfy, KindSlack, KindDiscord, KindWebhook}

// The types of event. A problem is something the needs-you list derived; a
// deploy event says how a deployment ended; test is what "Send a test" sends.
const (
	EventProblemOpened   = "problem.opened"
	EventProblemResolved = "problem.resolved"
	EventDeploySucceeded = "deploy.succeeded"
	EventDeployFailed    = "deploy.failed"
	EventTest            = "test"
)

// Event is one thing worth telling the owner. It is also the JSON body a
// webhook receives (plus the delivery's "id"), so the field names are part of
// the public API and are documented in api/openapi.yaml as NotificationEvent.
//
// Title and Text are the console's own wording; Detail is the raw reason. None
// of them holds a secret value.
type Event struct {
	Type  string `json:"type"`
	Kind  string `json:"kind,omitempty"` // the needs-you kind, for problems
	AppID string `json:"app,omitempty"`
	// Project, Environment and AppName say where the app is (feature 019): the
	// app's ID is these written out, so a receiver can route by environment
	// without parsing it. Empty for installation-wide events.
	Project     string  `json:"project,omitempty"`
	Environment string  `json:"environment,omitempty"`
	AppName     string  `json:"appName,omitempty"`
	Service     string  `json:"service,omitempty"`
	Title       string  `json:"title"`
	Text        string  `json:"text"`
	Detail      string  `json:"detail,omitempty"`
	Deployment  string  `json:"deployment,omitempty"`
	Version     *uint64 `json:"version,omitempty"`
	Outcome     string  `json:"outcome,omitempty"`
	Trigger     string  `json:"trigger,omitempty"`
	// PromotedFrom is the environment a promotion came from, for deploy events
	// of a deployment made by promoting another environment's.
	PromotedFrom string `json:"promotedFrom,omitempty"`
	// Origin is where a deploy came from, when its caller said (feature 020: a
	// CI run). Absent for deploys without it and for every other event.
	Origin *Origin `json:"origin,omitempty"`
	// Schedule is the schedule's name, for a problem about a schedule (feature
	// 022: schedule_failed, schedule_skipping) when it opens. ExitCode is the
	// code its failed run exited with, when it exited with one. Never the
	// run's output.
	Schedule string    `json:"schedule,omitempty"`
	ExitCode *int      `json:"exitCode,omitempty"`
	URL      string    `json:"url,omitempty"` // the console page about it, when its address is known
	At       time.Time `json:"at"`
}

// Origin is where a deploy came from, as a CI step told Caravana. It has the
// shape of app.Origin; this package depends on no other of Caravana's, so it
// has its own copy.
type Origin struct {
	Commit     string `json:"commit,omitempty"`
	Ref        string `json:"ref,omitempty"` // e.g. refs/heads/main
	Actor      string `json:"actor,omitempty"`
	Run        string `json:"run,omitempty"` // the CI run's URL
	Repository string `json:"repository,omitempty"`
}

// Channel is what sending to one channel needs, with its secret part read from
// where Caravana keeps it. What each field holds depends on the kind:
//
//	webhook: Address is the URL; Secret is the signing secret.
//	slack, discord: Address is the incoming-webhook URL, which is itself the
//	    secret; Secret is unused.
//	ntfy: Address is the topic URL; Secret is the access token, or empty.
type Channel struct {
	Kind    string
	Address string
	Secret  string
}
