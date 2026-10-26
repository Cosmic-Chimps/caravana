package ui

import (
	"strings"
	"testing"
)

// "Goes to" names the project when the app is outside Default › production, by the project's display
// name as the rail does, since two projects may each have a "web".
func TestDomainsGoesToNamesTheProject(t *testing.T) {
	e := shopConsole(t)
	e.deploy(t, shopWeb("staging", "latest"))
	e.deploy(t, whoamiApp())
	body := e.pageOf(t, "/domains")
	staging := text(dmRowOf(body, "staging.shop.example.com"))
	if !strings.Contains(staging, "Shop Floor › web :80 · staging") {
		t.Errorf("an app outside Default › production doesn't name its project:\n%s", staging)
	}
	home := text(dmRowOf(body, whoamiApp().Services[0].Domains[0].Host))
	if strings.Contains(home, "›") && strings.Contains(home, "Default") {
		t.Errorf("an app in Default › production names Default:\n%s", home)
	}
	if !strings.Contains(home, "whoami :80 · production") {
		t.Errorf("the home app's Goes to:\n%s", home)
	}
}

// The Options form's Path field says saving deploys the app, and the clock clause is the true one.
func TestDomainsHintsAndChecksClause(t *testing.T) {
	e := optionsEnv(t)
	row := text(dmRowOf(e.pageOf(t, "/domains"), "app.example.com"))
	if !strings.Contains(row, "Only requests under this path reach the app; the path is kept. Saving deploys the app.") {
		t.Errorf("the Path hint:\n%s", row)
	}
	if !strings.Contains(checksEvery, "while this page is open") {
		t.Errorf("the checks clause is %q", checksEvery)
	}
}
