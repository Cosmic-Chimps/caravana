// Package notify turns one event ("a backup failed", "a deploy ended") into
// the HTTP request a notification channel expects, and sends it (feature 018).
// It knows six kinds of channel: a signed JSON webhook, a Slack incoming
// webhook, a Discord webhook, an ntfy topic, a Telegram chat (through the Bot
// API) and email (through SMTP).
//
// The package depends on no other package of Caravana: callers hand it an
// Event and a Channel and get a request (or, for email, a Mail) back. Render
// and RenderMail are pure, so what each kind sends is pinned by golden files;
// Send and SendMail are the only functions that touch the network, and Deliver
// picks the right pair for a channel.
package notify

import "time"

// The kinds of channel.
const (
	KindWebhook  = "webhook"
	KindSlack    = "slack"
	KindDiscord  = "discord"
	KindNtfy     = "ntfy"
	KindEmail    = "email"
	KindTelegram = "telegram"
)

// Kinds lists the kinds of channel in the order the console offers them.
var Kinds = []string{KindNtfy, KindSlack, KindDiscord, KindWebhook, KindEmail, KindTelegram}

// The types of event. A problem is something the needs-you list derived (opened,
// worsened when its level rises, resolved); a deploy event says how a
// deployment ended; test is what "Send a test" sends.
const (
	EventProblemOpened   = "problem.opened"
	EventProblemWorsened = "problem.worsened" // a problem already sent got worse: a disk from warning to failed
	EventProblemResolved = "problem.resolved"
	EventDeploySucceeded = "deploy.succeeded"
	EventDeployFailed    = "deploy.failed"
	// EventCaravanaRelease says a newer Caravana release is out (feature 027):
	// sent once per release to every channel, and not a needs-you item, since
	// nothing is wrong.
	EventCaravanaRelease = "caravana.release"
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
	Schedule string `json:"schedule,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
	// Domain is the host of a problem about a domain (feature 023:
	// domain_dns_elsewhere, domain_cert_expiring) when it opens. AddressSeen
	// is the address its DNS gave that isn't this host's (dns_elsewhere);
	// ExpiresAt is when its certificate ends (cert_expiring).
	Domain      string     `json:"domain,omitempty"`
	AddressSeen string     `json:"addressSeen,omitempty"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
	// Mount is the filesystem of a problem about a disk (feature 025:
	// disk_nearly_full) when it opens, Holds what is on it ("nomad" and/or
	// "docker"), UsedPercent how full it is, FreeBytes what is left and Level
	// how bad it was then ("warning" from 85 %, "failed" from 95 %); a
	// problem.worsened carries the level it rose to. The host
	// has no app, so such an event has no app, project or environment.
	Mount       string   `json:"mount,omitempty"`
	Holds       []string `json:"holds,omitempty"`
	UsedPercent float64  `json:"usedPercent,omitempty"`
	FreeBytes   *int64   `json:"freeBytes,omitempty"`
	Level       string   `json:"level,omitempty"`
	// Server is the name of the server of a problem about a server (feature 031: server_down,
	// server_certificate_expiring) when it opens; ExpiresAt is then, for the certificate, when it
	// ends. Such an event has no app, project or environment.
	Server string `json:"server,omitempty"`
	// Package, PackageVersion, FixedIn, Advisory, Severity and Image are the facts of an
	// image_vulnerable problem (feature 044) when it opens: the package and its installed version,
	// the worst of its fixable advisories at or above the notify level (its ID, "critical" or "high"),
	// the versions that fix it (one entry each; any of them does) and the image's digest. Names and
	// versions from the scanner's database only.
	Package        string   `json:"package,omitempty"`
	PackageVersion string   `json:"packageVersion,omitempty"`
	FixedIn        []string `json:"fixedIn,omitempty"`
	Advisory       string   `json:"advisory,omitempty"`
	Severity       string   `json:"severity,omitempty"`
	Image          string   `json:"image,omitempty"`
	// Certificate is the ID of the uploaded certificate of a certificate_expiring problem (feature 034)
	// when it opens, and Names are the DNS names it covers; ExpiresAt is when it ends. Such an event
	// has no app, project or environment.
	Certificate string   `json:"certificate,omitempty"`
	Names       []string `json:"names,omitempty"`
	// Destination is the name of the backup destination of a problem about a backup (feature 052):
	// where a failed backup was going (backup_failed), or the one a failed copy was going to
	// (backup_copy_failed), when it opens.
	Destination string `json:"destination,omitempty"`
	// Release is the newer Caravana release of a caravana.release event.
	Release *Release  `json:"release,omitempty"`
	URL     string    `json:"url,omitempty"` // the console page about it, when its address is known; a caravana.release's is the release page
	At      time.Time `json:"at"`
}

// ReleaseTitle is the title of a caravana.release message: "New release:
// caravana 0.16.0". latest is written without a leading v.
func ReleaseTitle(latest string) string { return "New release: caravana " + latest }

// ReleaseSentence is the one wording of a newer release, the text of every
// notification: "caravana 0.16.0 is out. You run 0.15.0."
func ReleaseSentence(latest, current string) string {
	return "caravana " + latest + " is out. You run " + current + "."
}

// Release is what a caravana.release event says: the version that runs, the
// newer one and the release's page. Versions are written without a leading v.
type Release struct {
	Current string `json:"current"`
	Latest  string `json:"latest"`
	URL     string `json:"url"`
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
//	email: SMTP is the mail server and the addresses; Secret is its password, or
//	    empty. Address is unused.
//	telegram: Address is the Bot API server (DefaultTelegramAPI unless the owner
//	    runs their own), ChatID the chat to post to and Secret the bot token.
type Channel struct {
	Kind    string
	Address string
	Secret  string
	// SMTP is the mail server and the addresses of an email channel; Secret is
	// then its password, or empty.
	SMTP *SMTP
	// ChatID is the chat a telegram channel posts to; Address is then the Bot API
	// server (DefaultTelegramAPI unless the owner runs their own) and Secret the
	// bot token.
	ChatID string
}

// DefaultTelegramAPI is the Bot API's own server, the Address of a telegram
// channel unless the owner runs a local Bot API server.
const DefaultTelegramAPI = "https://api.telegram.org"

// How the connection to a mail server is secured.
const (
	SecuritySTARTTLS = "starttls" // plain connection, upgraded with STARTTLS (usually port 587)
	SecurityTLS      = "tls"      // TLS from the first byte (usually port 465)
	SecurityNone     = "none"     // no encryption; the service allows it only for this host
)

// SMTP is where an email channel sends and to whom.
type SMTP struct {
	Host     string
	Port     int
	Security string   // SecuritySTARTTLS, SecurityTLS or SecurityNone
	Username string   // empty: no AUTH
	From     string   // one address, with or without a display name
	To       []string // one or more addresses
}
