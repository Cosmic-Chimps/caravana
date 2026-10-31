package service

import "strings"

// FailureCause says, in words, why a deploy never reached Nomad, from the error text the service
// stored. The texts come from the sentinel errors of deploy and registry, which every such message
// contains. "" means the cause isn't one of the known ones; the raw error is shown under the sentence
// either way. It is the one wording of the needs-you list's "didn't start" sentence, of the app's
// summary and of the Deployments tab.
func FailureCause(errText string) string {
	// The registry's message carries the image's name in front ("ghcr.io: x:1: image not
	// found"), so these look for the phrase, not the start of the text.
	switch msg := strings.ToLower(errText); {
	case RegistryRefused(errText):
		return "the registry refused the stored credentials"
	case strings.Contains(msg, "image not found"):
		return "the image wasn't found"
	case strings.Contains(msg, "registry unavailable"):
		return "the registry couldn't be reached"
	case strings.Contains(msg, "nomad unavailable"):
		return "Nomad couldn't be reached"
	case strings.Contains(msg, "nomad rejected the job"):
		return "Nomad rejected the job"
	case ForeignJob(errText):
		return "a job with this name already exists in Nomad and caravana didn't create it"
	case strings.Contains(msg, "job conflict"):
		return "the Nomad job changed while deploying"
	}
	return ""
}

// RegistryRefused says whether a stored deploy error is the registry turning down the credentials
// Caravana sent (deploy.ErrRegistryAuth), which every such message carries. A missing image with
// credentials sent ("... or the stored credentials for X can't read it") is another error, about the
// image.
func RegistryRefused(errText string) bool {
	return strings.Contains(strings.ToLower(errText), "refused the stored credentials")
}

// ForeignJob says whether a stored error is the job conflict where Nomad already runs a job of that
// name which caravana didn't create. The other job conflict ("changed while deploying") is a race;
// the two need different words, and different fixes.
func ForeignJob(errText string) bool {
	msg := strings.ToLower(errText)
	return strings.Contains(msg, "job conflict") && !strings.Contains(msg, "changed while deploying")
}
