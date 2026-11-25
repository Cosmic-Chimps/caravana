package service

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/notify"
	"github.com/cosmic-chimps/caravana/internal/store"
)

func monoMarked(parts []TextPart) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Mono {
			b.WriteString("[" + p.Text + "]")
		} else {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func TestCertificateExpiringWords(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, tc := range []struct {
		name  string
		names []string
		end   time.Time
		want  string
	}{
		{"days", []string{"*.example.test"}, now.Add(5*day + 6*time.Hour),
			"The uploaded certificate for [*.example.test] expires in 5 days (Dec 2). Upload its replacement in Settings › Certificates."},
		{"one day", []string{"*.example.test"}, now.Add(day + time.Hour),
			"The uploaded certificate for [*.example.test] expires in 1 day (Nov 28). Upload its replacement in Settings › Certificates."},
		{"today", []string{"a.example.test"}, now.Add(3 * time.Hour),
			"The uploaded certificate for [a.example.test] expires today (Nov 27). Upload its replacement in Settings › Certificates."},
		{"expired", []string{"*.example.test"}, now.Add(-2 * day),
			"The uploaded certificate for [*.example.test] expired on Nov 25. Upload its replacement in Settings › Certificates."},
		{"more names", []string{"*.example.test", "example.test", "www.example.test"}, now.Add(3 * day),
			"The uploaded certificate for [*.example.test] and 2 more names expires in 3 days (Nov 30). Upload its replacement in Settings › Certificates."},
		{"one more name", []string{"*.example.test", "example.test"}, now.Add(3 * day),
			"The uploaded certificate for [*.example.test] and 1 more name expires in 3 days (Nov 30). Upload its replacement in Settings › Certificates."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			end := tc.end
			it := AttentionItem{Kind: AttentionCertificateExpiring, Certificate: "crt_0123456789ab", Names: tc.names, ExpiresAt: &end,
				Detail: certificateExpiringSentence(now, tc.names, end)}
			parts := ProblemParts(it, func(s string) string { return s })
			if got := monoMarked(parts); got != tc.want {
				t.Errorf("words = %q\nwant    %q", got, tc.want)
			}
			if plain := plainText(parts); plain != strings.NewReplacer("[", "", "]", "").Replace(tc.want) || plain != it.Detail {
				t.Errorf("plain = %q, detail = %q", plain, it.Detail)
			}
		})
	}
	// An item without its sentence still says something of its own.
	end := now.Add(3 * day)
	for _, it := range []AttentionItem{
		{Kind: AttentionCertificateExpiring, Names: []string{"a.example.test"}},
		{Kind: AttentionCertificateExpiring, Names: []string{"a.example.test"}, ExpiresAt: &end, Detail: "x"},
		{Kind: AttentionCertificateExpiring},
	} {
		got := plainText(ProblemParts(it, func(s string) string { return s }))
		if strings.Contains(got, "needs you (") || !strings.Contains(got, "Upload its replacement in Settings › Certificates.") {
			t.Errorf("fallback words = %q", got)
		}
	}
}

func TestCertificateExpiringNotification(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	end := now.Add(-time.Hour)
	it := AttentionItem{Kind: AttentionCertificateExpiring, Certificate: "crt_0123456789ab", Names: []string{"*.example.test", "example.test"},
		ExpiresAt: &end, Level: metrics.DiskFailed, When: now.Add(-48 * time.Hour),
		Detail: certificateExpiringSentence(now, []string{"*.example.test", "example.test"}, end)}
	ev := problemEvent(it, "https://console.example.com", now)
	if ev.Type != notify.EventProblemOpened || ev.Kind != "certificate_expiring" || ev.Certificate != "crt_0123456789ab" ||
		!slices.Equal(ev.Names, it.Names) || ev.ExpiresAt == nil || ev.AppID != "" ||
		ev.Title != "Certificate expired: *.example.test and 1 more name" || ev.Text != it.Detail ||
		ev.URL != "https://console.example.com/settings/certificates" {
		t.Errorf("event = %+v", ev)
	}
	it.Level = metrics.DiskWarning
	if got := problemEvent(it, "", now).Title; got != "Certificate expiring: *.example.test and 1 more name" {
		t.Errorf("title of an expiring certificate = %q", got)
	}
	if notJudged(AttentionCertificateExpiring, judged{}) {
		t.Error("certificate_expiring comes from SQLite: it is always judged")
	}
	// The key is one item per certificate.
	if k := problemKey(it); k != "certificate_expiring|||crt_0123456789ab" {
		t.Errorf("key = %q", k)
	}
	// Opened with the list, resolved once it is gone.
	ch := diffProblems(nil, []AttentionItem{it}, allJudged, map[string]bool{}, now, time.Minute)
	if len(ch.Opened) != 1 {
		t.Fatalf("opened = %+v", ch.Opened)
	}
	sent := []store.SentProblem{{Key: problemKey(it), Kind: string(it.Kind), LastSeenAt: now}}
	if ch := diffProblems(sent, nil, allJudged, map[string]bool{}, now.Add(time.Hour), time.Minute); len(ch.Resolved) != 1 {
		t.Errorf("resolved = %+v, want the certificate's problem", ch.Resolved)
	}
	if url := resolvedEvent(sent[0], "https://c.example", now).URL; url != "https://c.example/settings/certificates" {
		t.Errorf("a resolved event links to %q", url)
	}
}

// One item per upload inside the window or past its end; a valid one has none; replaced or removed,
// the item goes by itself.
func TestCertificateItemsFollowTheUploads(t *testing.T) {
	ctx := context.Background()
	e := certEnv(t)
	day := 24 * time.Hour
	add := func(id, name string, left time.Duration) {
		t.Helper()
		err := e.store.InsertUploadedCertificate(ctx, store.UploadedCertificate{ID: id, Names: []string{name}, Subject: name, Issuer: "Acme",
			NotBefore: time.Now().Add(-90 * day), NotAfter: time.Now().Add(left), Fingerprint: strings.Repeat(id[len(id)-1:], 64),
			KeyType: "ECDSA P-256", UploadedAt: time.Now().Add(-time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	add("crt_00000000000a", "ok.example.test", 60*day)
	add("crt_00000000000b", "soon.example.test", 5*day)
	add("crt_00000000000c", "gone.example.test", -2*day)

	att, err := e.svc.Attention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]metrics.DiskLevel{}
	for _, it := range att.Items {
		if it.Kind == AttentionCertificateExpiring {
			got[it.Certificate] = it.Level
			if it.AppID != "" || it.When.After(time.Now().Add(time.Minute)) {
				t.Errorf("item = %+v: it has no app and isn't from the future", it)
			}
		}
	}
	if len(got) != 2 || got["crt_00000000000b"] != metrics.DiskWarning || got["crt_00000000000c"] != metrics.DiskFailed {
		t.Errorf("items = %v, want soon as a warning and gone as failed", got)
	}

	// A wider window brings the valid one in; the window is the owner's setting.
	if err := e.svc.SetWarnBeforeDays(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if err := e.store.DeleteUploadedCertificate(ctx, "crt_00000000000c"); err != nil {
		t.Fatal(err)
	}
	att, _ = e.svc.Attention(ctx)
	var kinds []string
	for _, it := range att.Items {
		if it.Kind == AttentionCertificateExpiring {
			kinds = append(kinds, it.Certificate)
		}
	}
	if !slices.Equal(kinds, []string{"crt_00000000000b"}) {
		t.Errorf("after removing one: %v", kinds)
	}
}
