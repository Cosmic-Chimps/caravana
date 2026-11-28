package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/metrics"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The needs-you items of Caravana's own backup (feature 063, plan D7). They are about the installation, so
// they have no app, and their keys are host-level (kind|||). Like the rest of the list they are derived on
// every read and go by themselves when the cause does. None is raised while there is no backup destination:
// Backups are set up nowhere, and "no backup destination" speaks for that when an app asks for one.
//
//   - self_backup_no_key (a warning): a destination exists and no recovery key was made;
//   - self_backup_failed (failed): the newest run that ended failed, with its reason;
//   - self_backup_old (a warning): the daily backup is on, there is a key, and no run succeeded in the last
//     48 hours (or none ever, once 48 hours passed since the key was made). Not raised while turned off:
//     turning it off is the owner's choice, and the page says so.

// selfBackupItems is the three kinds' items.
func (s *Service) selfBackupItems(ctx context.Context) ([]AttentionItem, error) {
	dest, err := s.store.GetBackupDestination(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	set, err := s.store.SelfBackupSettings(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now().UTC()
	if !set.HasKey() {
		it := AttentionItem{Kind: AttentionSelfBackupNoKey, Level: metrics.DiskWarning, When: dest.UpdatedAt}
		it.Detail = plainText(selfBackupParts(it))
		return []AttentionItem{it}, nil
	}
	var items []AttentionItem
	if last, ok, err := s.store.LatestFinishedSelfBackup(ctx); err != nil {
		return nil, err
	} else if ok && last.Status == store.SelfBackupFailed {
		it := AttentionItem{Kind: AttentionSelfBackupFailed, Level: metrics.DiskFailed, When: last.EndedAt, Detail: last.Error}
		if it.When.IsZero() {
			it.When = last.StartedAt
		}
		items = append(items, it)
	}
	if set.Enabled {
		since := set.KeyCreatedAt // none ever: the key's age counts
		good, ok, err := s.store.LatestSucceededSelfBackup(ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			since = good.EndedAt
			if since.IsZero() {
				since = good.StartedAt
			}
		}
		if !since.IsZero() && now.Sub(since) > selfBackupOldAfter {
			it := AttentionItem{Kind: AttentionSelfBackupOld, Level: metrics.DiskWarning, When: since.Add(selfBackupOldAfter)}
			if ok {
				it.SelfBackupHours = int(now.Sub(since) / time.Hour)
			}
			it.Detail = plainText(selfBackupParts(it))
			items = append(items, it)
		}
	}
	return items, nil
}

// selfBackupParts is the three items' one wording. The failed item's Detail is the run's sentence, in
// Caravana's words, which says what went wrong.
func selfBackupParts(it AttentionItem) []TextPart {
	switch it.Kind {
	case AttentionSelfBackupNoKey:
		return parts("Caravana can't back itself up until you make a recovery key.")
	case AttentionSelfBackupFailed:
		reason := strings.TrimSpace(it.Detail)
		if reason == "" {
			return parts("The latest backup of Caravana itself failed.")
		}
		return parts("The latest backup of Caravana itself failed: " + reason)
	}
	if it.SelfBackupHours == 0 {
		return parts("Caravana has not backed itself up since its recovery key was made.")
	}
	if it.SelfBackupHours < 72 {
		return parts(fmt.Sprintf("The last backup of Caravana itself was %d hours ago.", it.SelfBackupHours))
	}
	return parts(fmt.Sprintf("The last backup of Caravana itself was %d days ago.", it.SelfBackupHours/24))
}

// selfBackupTitle is the notification's title for the three kinds.
func selfBackupTitle(k AttentionKind) string {
	switch k {
	case AttentionSelfBackupNoKey:
		return "Caravana's backup has no recovery key"
	case AttentionSelfBackupFailed:
		return "Caravana's backup failed"
	}
	return "Caravana's backup is out of date"
}
