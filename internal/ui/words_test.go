package ui

import (
	"context"
	"net/http"
	"regexp"
	"testing"
)

// TestNoShipWords guards a wording decision: the handoff's "Ship" and "Shipping" are not the
// console's words (feature 007 named the tab Deployments, and Deploy is the action). A page that
// says "ship" in any form fails here, so the word can't come back with a new panel or card.
func TestNoShipWords(t *testing.T) {
	e := newConsoleEnv(t)
	if _, err := e.Svc.CreateAppOnly(context.Background(), duoApp()); err != nil {
		t.Fatal(err)
	}
	ship := regexp.MustCompile(`(?i)\bship`)
	for _, path := range []string{
		"/apps", "/projects", "/apps/new",
		"/apps/duo", "/apps/duo/deployments", "/apps/duo/configure", "/apps/duo/variables",
		"/apps/duo/domains", "/apps/duo/backups", "/apps/duo/logs",
		"/settings", "/settings/registries", "/settings/backup-storage", "/settings/notifications", "/settings/tokens",
		"/schedules", "/schedules/new", "/logs", "/monitoring",
	} {
		resp, body := e.get(t, path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d", path, resp.StatusCode)
			continue
		}
		if m := ship.FindString(text(body)); m != "" {
			t.Errorf("GET %s says %q: the console's words are deploy and deployments", path, m)
		}
	}
}
