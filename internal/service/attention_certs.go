package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// certificate_expiring (feature 034, research R10): an uploaded certificate ends inside the warning
// window, or has ended, and nothing renews it. One item per certificate, not one per domain: a
// wildcard on twenty hosts is one problem. Derived on every read from SQLite, so it goes by itself
// when the certificate is replaced or removed. A warning; failed once the certificate has ended.
//
// The key is certificate_expiring|||<id>. A domain that presents an uploaded certificate raises no
// domain_cert_expiring: this item speaks for it.

// certificateItems is the kind's items for the stored uploads.
func (s *Service) certificateItems(ctx context.Context) ([]AttentionItem, error) {
	list, err := s.store.ListUploadedCertificates(ctx)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	warn, err := s.warnWindow(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	var items []AttentionItem
	for _, c := range list {
		status := certStatus(c.NotAfter, now, warn)
		if status == UploadValid {
			continue
		}
		it := AttentionItem{Kind: AttentionCertificateExpiring, Certificate: c.ID, Names: c.Names,
			ExpiresAt: new(c.NotAfter.UTC()), Level: metrics.DiskWarning}
		if status == UploadExpired {
			it.Level = metrics.DiskFailed
		}
		// It has been an item since it entered the window, and never from the future.
		it.When = c.NotAfter.Add(-warn)
		if it.When.After(now) {
			it.When = now
		}
		it.Detail = certificateExpiringSentence(now, c.Names, c.NotAfter)
		items = append(items, it)
	}
	return items, nil
}

// withoutUploadedExpiring drops the domain_cert_expiring items of domains whose last check
// presented an uploaded certificate: certificate_expiring speaks for them.
func (s *Service) withoutUploadedExpiring(ctx context.Context, items []AttentionItem, checks []store.DomainCheckRecord) ([]AttentionItem, error) {
	if !slices.ContainsFunc(items, func(it AttentionItem) bool { return it.Kind == AttentionDomainCertExpiring }) {
		return items, nil
	}
	uploads, err := s.uploadsByFingerprint(ctx)
	if err != nil || len(uploads) == 0 {
		return items, err
	}
	uploaded := map[string]bool{} // hosts whose check presented an upload
	for _, c := range checks {
		if _, ok := uploads[c.CertFingerprint]; ok && c.CertFingerprint != "" {
			uploaded[c.Host] = true
		}
	}
	return slices.DeleteFunc(items, func(it AttentionItem) bool {
		return it.Kind == AttentionDomainCertExpiring && uploaded[it.Domain]
	}), nil
}

// certificateNames says which names a certificate item is about: the first, and how many more.
func certificateNames(names []string) (first, more string) {
	if len(names) == 0 {
		return "", ""
	}
	first = names[0]
	switch n := len(names) - 1; n {
	case 0:
	case 1:
		more = " and 1 more name"
	default:
		more = fmt.Sprintf(" and %d more names", n)
	}
	return first, more
}

// certificateTail says when it ends, and what to do.
func certificateTail(now time.Time, notAfter time.Time) string {
	const action = " Upload its replacement in Settings › Certificates."
	left := notAfter.Sub(now)
	days := int(left / (24 * time.Hour))
	date := notAfter.UTC().Format("Jan 2")
	switch {
	case left <= 0:
		return "expired on " + date + "." + action
	case days == 0:
		return "expires today (" + date + ")." + action
	case days == 1:
		return "expires in 1 day (" + date + ")." + action
	}
	return fmt.Sprintf("expires in %d days (%s).%s", days, date, action)
}

// certificateExpiringSentence is the item's plain sentence: "The uploaded certificate for
// *.example.test expires in 5 days (Dec 2). Upload its replacement in Settings › Certificates."
func certificateExpiringSentence(now time.Time, names []string, notAfter time.Time) string {
	first, more := certificateNames(names)
	return "The uploaded certificate for " + first + more + " " + certificateTail(now, notAfter)
}

// certificateExpiringParts is the item's one wording, with the first name in mono. Detail is the
// plain sentence the item was made with; an item without one (or with another) says it needs
// replacing, with the date when ExpiresAt is known.
func certificateExpiringParts(it AttentionItem) []TextPart {
	first, more := certificateNames(it.Names)
	prefix := "The uploaded certificate for " + first + more + " "
	if tail, ok := strings.CutPrefix(it.Detail, prefix); ok && first != "" {
		return parts("The uploaded certificate for ", mono(first), more+" ", tail)
	}
	tail := "needs replacing. Upload its replacement in Settings › Certificates."
	if it.ExpiresAt != nil {
		tail = "ends on " + it.ExpiresAt.UTC().Format("Jan 2") + "." + " Upload its replacement in Settings › Certificates."
	}
	if first == "" {
		return parts("An uploaded certificate ", tail)
	}
	return parts("The uploaded certificate for ", mono(first), more+" ", tail)
}

// certificateTitle is the notification's title: "Certificate expiring: *.example.test".
func certificateTitle(it AttentionItem) string {
	first, more := certificateNames(it.Names)
	word := "expiring"
	if it.Level == metrics.DiskFailed {
		word = "expired"
	}
	return "Certificate " + word + ": " + first + more
}
