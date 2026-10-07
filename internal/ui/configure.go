package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// This file is what the Configure tab shows: the stored definition as plain
// facts, the webhook, and the delete section. The handlers are in app.go.

// What a health check does when it leaves its interval or timeout unset (the
// documented defaults of app.HealthCheck).
const (
	defaultCheckInterval = 10 * time.Second
	defaultCheckTimeout  = 2 * time.Second
)

// defRow is one fact of the definition: a label and its values (each a mono
// line). Note is a line of small print under the values. A fact with no
// values isn't drawn as a row: its label goes into the panel's one "Using
// defaults" line (defPanelOf).
type defRow struct {
	Label  string
	Values []string
	Note   string
}

// defPanel is one panel of the definition: the app's own facts, or one
// service's. Defaults names the facts nobody set (no value, or the default
// one), which share one line instead of a row each.
type defPanel struct {
	Title    string
	Rows     []defRow
	Defaults []string
}

// defPanelOf makes a panel from its facts, folding those with no value into
// Defaults.
func defPanelOf(title string, rows []defRow) defPanel {
	p := defPanel{Title: title}
	for _, r := range rows {
		if len(r.Values) == 0 {
			p.Defaults = append(p.Defaults, defaultName(r.Label))
			continue
		}
		p.Rows = append(p.Rows, r)
	}
	return p
}

// defaultName is a label as it reads in the middle of a sentence: "Health
// check" becomes "health check", but "CPU" stays.
func defaultName(label string) string {
	if len(label) > 1 && label[1] >= 'A' && label[1] <= 'Z' {
		return label
	}
	return strings.ToLower(label[:1]) + label[1:]
}

// webhookView is the webhook section. URL is the whole webhook address with
// its token, set only in the response that created the token (it is shown
// once and never stored).
type webhookView struct {
	Enabled bool
	Created when
	URL     string
}

// deleteView is the delete section. Goes says what will be removed; Error is
// about the name field, Notice about the delete as a whole (Nomad could not
// do its part). Either one opens the confirmation, so the message is in view.
type deleteView struct {
	ID     string
	Goes   sentence
	Error  sentence
	Notice sentence
}

func (d deleteView) Open() bool { return len(d.Error) > 0 || len(d.Notice) > 0 }

// configureTab is the body of the Configure tab.
type configureTab struct {
	Panels  []defPanel
	APIHint sentence
	Secrets secretsView
	Webhook webhookView
	Delete  deleteView
}

// secretsView is the Secrets section: each service's secret names, and the
// one form that sets a secret. No value is ever in it (FR-016).
type secretsView struct {
	Services []secretService
	Many     bool // more than one service: the form asks which
	Form     secretForm
}

// secretService is one service's secret names, in their stored order.
type secretService struct {
	Name  string
	Names []string
}

// secretForm is what the set form shows after a refusal: the service and
// name typed (never the value), and what was wrong.
type secretForm struct {
	Service, Name         string
	NameError, ValueError sentence
	Notice                sentence
}

// secretsOf lays out the stored app's secret names for the Secrets section.
func secretsOf(a app.App, form secretForm) secretsView {
	v := secretsView{Many: len(a.Services) > 1, Form: form}
	for _, svc := range a.Services {
		s := secretService{Name: svc.Name}
		for _, sec := range svc.Secrets {
			s.Names = append(s.Names, sec.Name)
		}
		v.Services = append(v.Services, s)
	}
	return v
}

// definitionOf lays the stored app out as panels: the app, then one per
// service, in order. Every field of app.App is in one of them, except the
// values of environment variables, which are never drawn (FR-015).
func definitionOf(a app.App) []defPanel {
	appRows := []defRow{{Label: "ID", Values: []string{a.ID}}}
	if a.Name != "" {
		appRows = append(appRows, defRow{Label: "Display name", Values: []string{a.Name}})
	}
	appRows = append(appRows, defRow{Label: "Source", Values: []string{string(a.Source.Kind)}})
	panels := []defPanel{defPanelOf("App", appRows)}

	for _, svc := range a.Services {
		panels = append(panels, defPanelOf(svc.Name, serviceRows(svc)))
	}
	return panels
}

func serviceRows(svc app.Service) []defRow {
	rows := []defRow{
		{Label: "Image", Values: []string{svc.Image}},
		{Label: "Command", Values: nonEmpty(svc.Command)},
		{Label: "Arguments", Values: svc.Args},
	}

	var ports, domains, env []string
	for _, p := range svc.Ports {
		ports = append(ports, p.Name+" → "+strconv.Itoa(p.ContainerPort))
	}
	for _, d := range svc.Domains {
		domains = append(domains, d.Host+" → "+d.Port)
	}
	for _, v := range svc.Env {
		env = append(env, v.Name)
	}
	rows = append(rows,
		defRow{Label: "Ports", Values: ports},
		defRow{Label: "Domains", Values: domains},
		defRow{Label: "Copies", Values: []string{strconv.Itoa(replicasOf(svc))}},
		defRow{Label: "CPU", Values: unitValue(svc.Resources.CPUMHz, "MHz")},
		defRow{Label: "Memory", Values: unitValue(svc.Resources.MemoryMB, "MB")},
		defRow{Label: "Health check", Values: nonEmpty(healthWords(svc.Health))},
	)
	envRow := defRow{Label: "Environment variables", Values: env}
	if len(env) > 0 {
		envRow.Note = "Values aren't shown."
	}
	rows = append(rows, envRow)
	// No volumes isn't a default, so a service without any has no row and
	// isn't named on the "Using defaults" line either.
	if len(svc.Volumes) == 0 {
		return rows
	}
	var volumes []string
	for _, v := range svc.Volumes {
		s := v.Name + " → " + v.Path
		if v.ReadOnly {
			s += " (read-only)"
		}
		volumes = append(volumes, s)
	}
	return append(rows, defRow{Label: "Volumes", Values: volumes, Note: "Kept across deploys, erased with the app."})
}

// nonEmpty is a one-value list, or none for "": a fact with no values goes
// into the panel's "Using defaults" line.
func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// unitValue is "250 MHz", or none for a field left at zero (the default).
func unitValue(n int, unit string) []string {
	if n == 0 {
		return nil
	}
	return []string{strconv.Itoa(n) + " " + unit}
}

// replicasOf is how many copies the service asks for: a missing count means 1.
func replicasOf(svc app.Service) int {
	if svc.Replicas == nil {
		return 1
	}
	return *svc.Replicas
}

// healthWords says how a service is checked: "GET /healthz every 10 s,
// timeout 2 s", "TCP every 10 s, timeout 2 s", or "" for no check.
func healthWords(h *app.HealthCheck) string {
	if h == nil {
		return ""
	}
	interval, timeout := h.Interval, h.Timeout
	if interval == 0 {
		interval = defaultCheckInterval
	}
	if timeout == 0 {
		timeout = defaultCheckTimeout
	}
	what := "TCP"
	if h.Path != "" {
		what = "GET " + h.Path
	}
	if h.Port != "" {
		what += " on port " + h.Port
	}
	return fmt.Sprintf("%s every %s, timeout %s", what, durationText(interval), durationText(timeout))
}

// whatGoes is the sentence that says what deleting the app removes (research
// R16): its copies stop, its addresses stop answering, its history and its
// webhook token are erased.
func whatGoes(a app.App, sum service.AppSummary, deployments int, hasToken bool) sentence {
	copies := sum.Status.WantedCopies
	verb := "stop"
	if copies == 1 {
		verb = "stops"
	}
	s := sentence{plain("Its " + copyCount(copies) + " " + verb)}
	var hosts []string
	for _, svc := range a.Services {
		for _, d := range svc.Domains {
			hosts = append(hosts, d.Host)
		}
	}
	if len(hosts) > 0 {
		s = append(s, plain(" and "))
		for i, h := range hosts {
			switch {
			case i == 0:
			case i == len(hosts)-1:
				s = append(s, plain(" and "))
			default:
				s = append(s, plain(", "))
			}
			s = append(s, mono(h))
		}
		if len(hosts) == 1 {
			s = append(s, plain(" stops answering."))
		} else {
			s = append(s, plain(" stop answering."))
		}
	} else {
		s = append(s, plain("."))
	}

	var erased []string
	if deployments > 0 {
		erased = append(erased, plural(deployments, "deployment"))
	}
	if hasToken {
		erased = append(erased, "webhook token")
	}
	if len(erased) > 0 {
		be := "are"
		if len(erased) == 1 && (hasToken || deployments == 1) {
			be = "is"
		}
		s = append(s, plain(" Its "+strings.Join(erased, " and its ")+" "+be+" erased."))
	}

	var volumes []string
	for _, svc := range a.Services {
		for _, v := range svc.Volumes {
			volumes = append(volumes, v.Name)
		}
	}
	if len(volumes) > 0 {
		word, them := "volume", "it"
		if len(volumes) > 1 {
			word, them = "volumes", "them"
		}
		s = append(s, plain(" Its "+word+" "))
		for i, v := range volumes {
			switch {
			case i == 0:
			case i == len(volumes)-1:
				s = append(s, plain(" and "))
			default:
				s = append(s, plain(", "))
			}
			s = append(s, mono(v))
		}
		s = append(s, plain(" and the data in "+them+" are erased."))
	}
	return append(s, plain(" This can't be undone."))
}
