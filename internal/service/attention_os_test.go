package service

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/hostos"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

var osNow0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// osState is a readable Ubuntu host with a list of waiting updates checked
// listAge ago.
func osHost(total, security int, listAge time.Duration, auto *bool) hostos.State {
	return hostos.State{
		Readable: true, Distro: hostos.Distro{ID: "ubuntu", Version: "24.04"},
		Updates:    hostos.Updates{Known: true, Total: total, Security: security, CheckedAt: osNow0.Add(-listAge)},
		Unattended: hostos.Unattended{Enabled: auto},
	}
}

func TestOSItemsOf(t *testing.T) {
	on, off := true, false
	reboot := func(st hostos.State, installed string, pkgs ...string) hostos.State {
		st.Reboot = hostos.Reboot{Required: true, Since: osNow0.Add(-time.Hour), Packages: pkgs, InstalledKernel: installed}
		return st
	}
	tests := []struct {
		name    string
		state   hostos.State
		running string
		since   time.Time
		want    []string // kind: sentence
	}{
		{"nothing waiting", osHost(0, 0, time.Hour, &on), "6.8.0-147-generic", time.Time{}, nil},
		{"a kernel installed and not running",
			reboot(osHost(0, 0, time.Hour, &on), "6.8.0-149-generic", "linux-image-6.8.0-149-generic"), "6.8.0-147-generic", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running."}},
		{"the kernel sentence needs no package list",
			reboot(osHost(0, 0, time.Hour, &on), "6.8.0-149-generic"), "6.8.0-147-generic", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running."}},
		{"a package other than a kernel",
			reboot(osHost(0, 0, time.Hour, &on), "6.8.0-147-generic", "libc6"), "6.8.0-147-generic", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: libc6 was updated and needs a reboot."}},
		{"two packages", reboot(osHost(0, 0, time.Hour, &on), "", "libc6", "linux-base"), "", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: libc6 and linux-base were updated and need a reboot."}},
		{"five packages", reboot(osHost(0, 0, time.Hour, &on), "", "a", "b", "c", "d", "e"), "", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: a, b, c and 2 more were updated and need a reboot."}},
		{"a reboot with no names and no kernel", reboot(osHost(0, 0, time.Hour, &on), ""), "", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot."}},
		{"an installed kernel that is not newer is not the reason",
			reboot(osHost(0, 0, time.Hour, &on), "6.8.0-147-generic", "libc6"), "6.8.0-149-generic", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: libc6 was updated and needs a reboot."}},
		{"a reboot is pending even where the files aren't Ubuntu's",
			func() hostos.State {
				return hostos.State{Reboot: hostos.Reboot{Required: true, Since: osNow0, Packages: []string{"libc6"}}}
			}(), "", time.Time{},
			[]string{"os_reboot_required: This server needs a reboot: libc6 was updated and needs a reboot."}},

		{"security updates, the host's own updates off: at once", osHost(14, 9, time.Hour, &off), "", time.Time{},
			[]string{"os_security_updates: 14 updates are waiting, 9 of them security updates."}},
		{"security updates, the setting unknown counts as off", osHost(14, 9, time.Hour, nil), "", time.Time{},
			[]string{"os_security_updates: 14 updates are waiting, 9 of them security updates."}},
		{"automatic updates on, waiting 23 hours: not yet", osHost(14, 9, time.Hour, &on), "", osNow0.Add(-23 * time.Hour), nil},
		{"automatic updates on, waiting 24 hours: now", osHost(14, 9, time.Hour, &on), "", osNow0.Add(-24 * time.Hour),
			[]string{"os_security_updates: 14 updates are waiting, 9 of them security updates."}},
		{"automatic updates on and the clock not known: not yet", osHost(14, 9, time.Hour, &on), "", time.Time{}, nil},
		{"a list 48 hours old is still judged", osHost(14, 9, 48*time.Hour, &off), "", time.Time{},
			[]string{"os_security_updates: 14 updates are waiting, 9 of them security updates."}},
		{"a list past 48 hours is not judged", osHost(14, 9, 48*time.Hour+time.Minute, &off), "", time.Time{}, nil},
		{"no security updates among them", osHost(14, 0, time.Hour, &off), "", time.Time{}, nil},
		{"one update, and it is security", osHost(1, 1, time.Hour, &off), "", time.Time{},
			[]string{"os_security_updates: 1 update is waiting, and it is a security update."}},
		{"all of them security", osHost(9, 9, time.Hour, &off), "", time.Time{},
			[]string{"os_security_updates: 9 updates are waiting, all of them security updates."}},
		{"one of many", osHost(14, 1, time.Hour, &off), "", time.Time{},
			[]string{"os_security_updates: 14 updates are waiting, 1 of them a security update."}},
		{"an unreadable host has no counts", func() hostos.State { s := osHost(14, 9, time.Hour, &off); s.Readable = false; return s }(), "", time.Time{}, nil},
		{"a count that isn't known", func() hostos.State { s := osHost(14, 9, time.Hour, &off); s.Updates.Known = false; return s }(), "", time.Time{}, nil},
		{"both items",
			reboot(osHost(14, 9, time.Hour, &off), "6.8.0-149-generic"), "6.8.0-147-generic", time.Time{},
			[]string{
				"os_reboot_required: This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running.",
				"os_security_updates: 14 updates are waiting, 9 of them security updates.",
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, it := range osItemsOf(osNow0, tc.state, tc.running, tc.since) {
				got = append(got, string(it.Kind)+": "+it.Detail)
				// Whatever the words, no item names a vulnerability.
				if strings.Contains(strings.ToLower(it.Detail), "cve") {
					t.Errorf("a sentence names a CVE: %q", it.Detail)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("items = %q\nwant    %q", got, tc.want)
			}
		})
	}
}

// The items' words in parts say the same as the plain sentence.
func TestOSItemParts(t *testing.T) {
	for name, it := range map[string]AttentionItem{
		"kernel":    {Kind: AttentionOSRebootRequired, InstalledKernel: "6.8.0-149-generic", RunningKernel: "6.8.0-147-generic"},
		"one":       {Kind: AttentionOSRebootRequired, Packages: []string{"libc6"}},
		"two":       {Kind: AttentionOSRebootRequired, Packages: []string{"libc6", "linux-base"}},
		"three":     {Kind: AttentionOSRebootRequired, Packages: []string{"a", "b", "c"}},
		"many":      {Kind: AttentionOSRebootRequired, Packages: []string{"a", "b", "c", "d"}},
		"none":      {Kind: AttentionOSRebootRequired},
		"security":  {Kind: AttentionOSSecurityUpdates, Updates: 14, Security: 9},
		"security1": {Kind: AttentionOSSecurityUpdates, Updates: 1, Security: 1},
	} {
		want := ""
		if it.Kind == AttentionOSRebootRequired {
			want = rebootSentence(it)
		} else {
			want = securitySentence(it.Updates, it.Security)
		}
		got := ProblemParts(it, func(s string) string { return s })
		if plainText(got) != want {
			t.Errorf("%s: parts say %q, the sentence is %q", name, plainText(got), want)
		}
	}
	// The kernels and the packages are the mono parts.
	p := ProblemParts(AttentionItem{Kind: AttentionOSRebootRequired, InstalledKernel: "6.8.0-149-generic", RunningKernel: "6.8.0-147-generic"}, nil)
	var monos []string
	for _, x := range p {
		if x.Mono {
			monos = append(monos, x.Text)
		}
	}
	if !slices.Equal(monos, []string{"6.8.0-149", "6.8.0-147"}) {
		t.Errorf("mono parts = %v", monos)
	}
}

// osRig is a notify env whose OS files are a fake root and whose clock moves.
type osRig struct {
	*notifyEnv
	files fstest.MapFS
}

func newOSRig(t *testing.T) *osRig {
	t.Helper()
	r := &osRig{notifyEnv: newNotifyEnv(t), files: fstest.MapFS{}}
	r.clockAt(osNow0)
	r.svc.resolveAfter = 5 * time.Minute
	r.svc.publicURL = "https://caravana.example.com"
	r.svc.sampler.node = &HostNode{NodeFacts: deploy.NodeFacts{Kernel: "6.8.0-147-generic"}}
	r.setFiles(t, osNow0, false, 0, 0, true)
	return r
}

// setFiles writes the host's files: an Ubuntu os-release, the list of updates
// (checked at listAt, skipped when total is negative), the reboot marker when
// reboot, and unattended-upgrades on or off.
func (r *osRig) setFiles(t *testing.T, listAt time.Time, reboot bool, total, security int, auto bool) {
	t.Helper()
	files := fstest.MapFS{"etc/os-release": {Data: []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")}}
	if total >= 0 {
		lines := ""
		if total > 0 || security == 0 {
			lines += "\n" + strconv.Itoa(total) + " updates can be applied immediately.\n"
		}
		if security > 0 {
			lines += strconv.Itoa(security) + " of these updates are standard security updates.\n"
		}
		files["var/lib/update-notifier/updates-available"] = &fstest.MapFile{Data: []byte(lines), ModTime: listAt}
	}
	if reboot {
		files["run/reboot-required"] = &fstest.MapFile{Data: []byte("*** System restart required ***\n"), ModTime: r.svc.clock.Now()}
		files["run/reboot-required.pkgs"] = &fstest.MapFile{Data: []byte("linux-image-6.8.0-149-generic\n")}
		files["boot/vmlinuz-6.8.0-149-generic"] = &fstest.MapFile{}
	}
	value := "0"
	if auto {
		value = "1"
	}
	files["etc/apt/apt.conf.d/20auto-upgrades"] = &fstest.MapFile{Data: []byte("APT::Periodic::Unattended-Upgrade \"" + value + "\";\n")}
	r.files = files
	r.svc.SetHostRoot(files)
}

// tickOS is one pass of the sampler's OS part, then one check of the list.
func (r *osRig) tickOS(t *testing.T) {
	t.Helper()
	r.svc.noteOS(context.Background())
	r.check(t)
}

func (r *osRig) sent(t *testing.T) []store.Delivery { return r.deliveriesOf(t, r.problems) }

// The story of the reboot (the case this page exists for): the item opens with
// one message, nothing while it lasts, and one "resolved" after the host
// rebooted and the item has been gone for the delay.
func TestOSRebootStory(t *testing.T) {
	r := newOSRig(t)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 0 || len(r.sentKeys(t)) != 0 {
		t.Fatalf("a clean host sent %v", problemTypes(got))
	}

	r.setFiles(t, osNow0, true, 0, 0, true)
	r.tickOS(t)
	r.tickOS(t)
	got := r.sent(t)
	if len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("a pending reboot: %v, want one problem.opened", problemTypes(got))
	}
	ev := payloadOf(t, got[0])
	if ev.Kind != "os_reboot_required" || ev.AppID != "" || ev.Title != "Reboot needed" ||
		ev.Text != "This server needs a reboot: kernel 6.8.0-149 is installed, but 6.8.0-147 is still running." ||
		ev.Detail != "" || ev.URL != "https://caravana.example.com/ops/updates" {
		t.Errorf("message = %+v", ev)
	}
	if keys := r.sentKeys(t); !slices.Equal(keys, []string{"os_reboot_required|||"}) {
		t.Errorf("sent problems = %v", keys)
	}
	att, err := r.svc.Attention(context.Background())
	if err != nil || att.OSUnknown || len(att.Items) != 1 || att.Items[0].Kind != AttentionOSRebootRequired {
		t.Fatalf("Attention = %+v, %v", att, err)
	}
	// Channels that don't want problems, and the paused one, got nothing.
	if n := len(r.deliveriesOf(t, r.onlyDeploy)) + len(r.deliveriesOf(t, r.paused)); n != 0 {
		t.Errorf("other channels got %d messages", n)
	}

	// The host reboots: the marker is gone. Not resolved yet, then resolved once.
	r.setFiles(t, osNow0, false, 0, 0, true)
	r.svc.clock.(*fakeClock).Advance(time.Minute)
	r.tickOS(t)
	r.svc.clock.(*fakeClock).Advance(3 * time.Minute)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 1 {
		t.Fatalf("gone for 3 minutes: %v, want nothing more yet", problemTypes(got))
	}
	r.svc.clock.(*fakeClock).Advance(2 * time.Minute)
	r.tickOS(t)
	r.tickOS(t)
	got = r.sent(t)
	if len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("gone for the delay: %v, want opened then resolved", problemTypes(got))
	}
	if ev := payloadOf(t, got[1]); ev.Title != "Resolved: Reboot needed" || ev.URL != "https://caravana.example.com/ops/updates" {
		t.Errorf("resolved = %+v", ev)
	}
	if keys := r.sentKeys(t); len(keys) != 0 {
		t.Errorf("sent problems after resolving = %v", keys)
	}
}

// A host nobody has read lately is not judged: no "opened" from a reading that
// is old, and no "resolved" for a marker nobody is looking at.
func TestOSItemsAreNotJudgedWhenTheReadingIsStale(t *testing.T) {
	r := newOSRig(t)

	// Before any reading: unknown, nothing sent.
	r.check(t)
	att, _ := r.svc.Attention(context.Background())
	if !att.OSUnknown {
		t.Error("before the first reading the OS kinds are judged")
	}

	r.setFiles(t, osNow0, true, 0, 0, true)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 1 {
		t.Fatalf("opened: %v", problemTypes(got))
	}

	// The sampler stops for an hour, and the marker is long gone from a rebooted
	// host nobody reads: not resolved, not opened again.
	r.setFiles(t, osNow0, false, 0, 0, true)
	for range 12 {
		r.svc.clock.(*fakeClock).Advance(5 * time.Minute)
		r.check(t)
	}
	att, _ = r.svc.Attention(context.Background())
	if !att.OSUnknown || len(att.Items) != 0 {
		t.Fatalf("Attention with an old reading = %+v; want the OS unknown and no item", att)
	}
	if got := r.sent(t); len(got) != 1 || len(r.sentKeys(t)) != 1 {
		t.Fatalf("an outage: %v and %d rows, want 1 and 1", problemTypes(got), len(r.sentKeys(t)))
	}

	// The sampler is back: the first check that can judge resolves it, once.
	r.tickOS(t)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("after the outage: %v, want opened then resolved", problemTypes(got))
	}
}

// Security updates: with the host's own updates on, an item only after 24 hours;
// a list older than 48 hours judges nothing; the mark survives a restart.
func TestOSSecurityStory(t *testing.T) {
	ctx := context.Background()
	r := newOSRig(t)

	// 14 / 9 appear, the list fresh, automatic updates on.
	r.setFiles(t, r.svc.clock.Now(), false, 14, 9, true)
	r.tickOS(t)
	since, ok, err := r.store.HostMark(ctx, store.MarkSecuritySince)
	if err != nil || !ok || !since.Equal(osNow0) {
		t.Fatalf("mark = %v, %v, %v; want %v", since, ok, err, osNow0)
	}
	if got := r.sent(t); len(got) != 0 {
		t.Fatalf("security updates with automatic updates on, at once: %v", problemTypes(got))
	}

	// A restart doesn't start the clock again: the mark is read back.
	r.svc.clock.(*fakeClock).Advance(23 * time.Hour)
	r.svc.sampler.securitySince = time.Time{}
	r.setFiles(t, r.svc.clock.Now(), false, 14, 9, true)
	r.tickOS(t)
	if got := r.svc.securityClock(); !got.Equal(osNow0) {
		t.Errorf("after a restart the clock = %v, want %v", got, osNow0)
	}
	if got := r.sent(t); len(got) != 0 {
		t.Fatalf("23 hours: %v", problemTypes(got))
	}

	// 24 hours: it opens, once.
	r.svc.clock.(*fakeClock).Advance(time.Hour)
	r.setFiles(t, r.svc.clock.Now(), false, 14, 9, true)
	r.tickOS(t)
	r.tickOS(t)
	got := r.sent(t)
	if len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("24 hours: %v, want one problem.opened", problemTypes(got))
	}
	ev := payloadOf(t, got[0])
	if ev.Kind != "os_security_updates" || ev.Title != "Security updates waiting" ||
		ev.Text != "14 updates are waiting, 9 of them security updates." || ev.URL != "https://caravana.example.com/ops/updates" {
		t.Errorf("message = %+v", ev)
	}
	if keys := r.sentKeys(t); !slices.Equal(keys, []string{"os_security_updates|||"}) {
		t.Errorf("sent problems = %v", keys)
	}

	// Ubuntu's list goes stale (no apt update for 3 days): the item leaves, and
	// after the delay it is resolved; it is not judged "still waiting".
	r.svc.clock.(*fakeClock).Advance(72 * time.Hour)
	r.setFiles(t, r.svc.clock.Now().Add(-72*time.Hour), false, 14, 9, true)
	r.tickOS(t)
	r.svc.clock.(*fakeClock).Advance(6 * time.Minute)
	r.tickOS(t)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 2 || got[1].EventType != notify.EventProblemResolved {
		t.Fatalf("a stale list: %v, want opened then resolved", problemTypes(got))
	}

	// The updates are installed: zero, the mark is dropped.
	r.setFiles(t, r.svc.clock.Now(), false, 0, 0, true)
	r.tickOS(t)
	if _, ok, _ := r.store.HostMark(ctx, store.MarkSecuritySince); ok {
		t.Error("the mark stays after the count went to zero")
	}
	if !r.svc.securityClock().IsZero() {
		t.Error("the clock stays after the count went to zero")
	}

	// A file that says nothing leaves the mark alone.
	r.setFiles(t, r.svc.clock.Now(), false, 14, 9, true)
	r.tickOS(t)
	delete(r.files, "var/lib/update-notifier/updates-available")
	r.tickOS(t)
	if _, ok, _ := r.store.HostMark(ctx, store.MarkSecuritySince); !ok {
		t.Error("an unreadable list dropped the mark")
	}
}

// With the host's own updates off, the item opens at once.
func TestOSSecurityAtOnceWithAutomaticUpdatesOff(t *testing.T) {
	r := newOSRig(t)
	r.setFiles(t, r.svc.clock.Now(), false, 14, 9, false)
	r.tickOS(t)
	if got := r.sent(t); len(got) != 1 || got[0].EventType != notify.EventProblemOpened {
		t.Fatalf("automatic updates off: %v, want one problem.opened", problemTypes(got))
	}
}

func TestNoteOSPublishesUpdatesOnlyOnChange(t *testing.T) {
	r := newOSRig(t)
	notices, stop := r.svc.Subscribe("")
	defer stop()
	saw := func() bool {
		seen := false
		for {
			select {
			case c := <-notices:
				seen = seen || c.What == ChangeUpdates
			default:
				return seen
			}
		}
	}
	r.svc.noteOS(context.Background())
	if !saw() {
		t.Error("no updates notice for the first reading")
	}
	r.svc.noteOS(context.Background())
	if saw() {
		t.Error("an updates notice for nothing new")
	}
	r.setFiles(t, osNow0, true, 0, 0, true)
	r.svc.noteOS(context.Background())
	if !saw() {
		t.Error("no updates notice when a reboot became needed")
	}
}

// The service has the reading of the Updates page's OS block before the sampler
// has made one, and with it after.
func TestUpdatesOSBlock(t *testing.T) {
	r := newOSRig(t)
	r.setFiles(t, osNow0.Add(-3*time.Hour), true, 14, 9, true)
	v, err := r.svc.Updates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o := v.OS
	if !o.Readable || o.Distro.ID != "ubuntu" || !o.Updates.Known || o.Updates.Total != 14 || o.Updates.Security != 9 || o.Updates.Stale ||
		!o.Updates.CheckedAt.Equal(osNow0.Add(-3*time.Hour)) {
		t.Errorf("os = %+v", o)
	}
	if !o.Reboot.Required || o.Reboot.RunningKernel != "6.8.0-147-generic" || o.Reboot.InstalledKernel != "6.8.0-149-generic" ||
		!slices.Equal(o.Reboot.Packages, []string{"linux-image-6.8.0-149-generic"}) {
		t.Errorf("reboot = %+v", o.Reboot)
	}
	if o.Unattended.Enabled == nil || !*o.Unattended.Enabled {
		t.Errorf("unattended = %+v", o.Unattended)
	}

	// A list over 48 hours old is shown, stale.
	r.setFiles(t, osNow0.Add(-49*time.Hour), false, 14, 9, true)
	if v, _ = r.svc.Updates(context.Background()); !v.OS.Updates.Stale || v.OS.Updates.Total != 14 {
		t.Errorf("a 49-hour list = %+v", v.OS.Updates)
	}

	// A host that isn't Debian or Ubuntu: one sentence's worth of facts.
	r.svc.SetHostRoot(fstest.MapFS{"etc/os-release": {Data: []byte("ID=fedora\nVERSION_ID=40\n")}})
	v, _ = r.svc.Updates(context.Background())
	if v.OS.Readable || v.OS.Distro.ID != "fedora" || v.OS.Updates.Known || v.OS.Updates.PackagesAvailable {
		t.Errorf("a Fedora host = %+v", v.OS)
	}
}
