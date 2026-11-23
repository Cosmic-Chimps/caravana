package service

import (
	"context"
	"slices"

	"github.com/cosmic-chimps/caravana/internal/store"
)

// backupCopyItems derives backup_copy_failed (feature 052): for each service and destination its
// policy copies to, the newest copy that finished failed. A later copy that succeeds resolves it,
// as does a policy that no longer copies there or an app that is gone; a copy still waiting or
// running leaves the item as it was. The detail is the copy's message, which is rclone's words or
// Caravana's and never holds a key.
func (s *Service) backupCopyItems(ctx context.Context, apps []store.AppRecord) ([]AttentionItem, error) {
	latest, err := s.store.LatestBackupCopies(ctx)
	if err != nil {
		return nil, err
	}
	known := make(map[string]store.AppRecord, len(apps))
	for _, rec := range apps {
		known[rec.App.ID] = rec
	}
	var out []AttentionItem
	for _, c := range latest {
		rec, ok := known[c.AppID]
		if !ok || c.Status != store.CopyFailed || !copiesTo(rec, c.Service, c.Destination) {
			continue
		}
		out = append(out, AttentionItem{
			Kind: AttentionBackupCopyFailed, AppID: c.AppID, Service: c.Service, Destination: c.Destination,
			Project: rec.Place.Project, Environment: rec.Place.Environment, AppName: rec.Place.Name,
			When: finishedOr(c.FinishedAt, c.CreatedAt), Detail: c.Message,
		})
	}
	return out, nil
}

// copiesTo reports whether the service's policy copies its backups to the destination.
func copiesTo(rec store.AppRecord, service, destination string) bool {
	for _, svc := range rec.App.Services {
		if svc.Name == service {
			return svc.Backup != nil && slices.Contains(svc.Backup.CopyTo, destination)
		}
	}
	return false
}
