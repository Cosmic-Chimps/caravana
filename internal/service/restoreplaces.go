package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/nomadjob"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Restoring from a place (feature 052, plan D11). A backup is in its own
// destination and in each copy that succeeded and that retention hasn't deleted.
// A restore reads from one of these places.

// ErrBackupMissing means the backup is in none of its places any more: every
// place that could be asked says the object is gone.
var ErrBackupMissing = errors.New("the backup is in none of its places any more")

// restorePlace is one place a backup is: the destination's name and the object's
// path in it.
type restorePlace struct {
	Destination string
	Object      string
}

// restorePlaces lists the places that hold b, in the order a restore tries them:
// its own destination (own, unless retention deleted it there), then each copy
// that succeeded and is still there, in the order of the policy's copyTo (a copy
// the policy no longer names after the rest, in the order they were made).
func restorePlaces(b store.BackupRecord, own string, policy []string) []restorePlace {
	var out []restorePlace
	if !b.Expired && own != "" {
		out = append(out, restorePlace{Destination: own, Object: b.Object})
	}
	var copies []store.BackupCopy
	for _, c := range b.Copies {
		if c.Status == store.CopySucceeded && !c.Expired && c.Destination != own {
			copies = append(copies, c)
		}
	}
	rank := func(name string) int {
		if i := slices.Index(policy, name); i >= 0 {
			return i
		}
		return len(policy)
	}
	slices.SortStableFunc(copies, func(x, y store.BackupCopy) int { return rank(x.Destination) - rank(y.Destination) })
	for _, c := range copies {
		out = append(out, restorePlace{Destination: c.Destination, Object: c.Object})
	}
	return out
}

// namesOf lists the places' destinations, "main or offsite".
func namesOf(places []restorePlace) string {
	names := make([]string, len(places))
	for i, p := range places {
		names[i] = p.Destination
	}
	if len(names) <= 2 {
		return strings.Join(names, " or ")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// chooseRestorePlace picks the place a restore of b reads from. from names it
// (a *app.FieldError on "from" when the backup isn't there). Without from, one
// place is the answer; with more, a check job asks each in order which still
// holds the object, and the places it finds missing are marked expired. The
// errors include ErrBackupMissing, a store.ErrNotFound for a backup in no place
// at all, and ErrNoBackupDestination when the destination to read from was removed.
func (s *Service) chooseRestorePlace(ctx context.Context, appID string, b store.BackupRecord, svc app.Service, from string) (restorePlace, deploy.BackupDestination, error) {
	own := b.Destination
	if own == "" {
		// A record from before destinations had names: where the policy sends backups now.
		var err error
		if own, _, err = s.backupPlaces(ctx, appID, *svc.Backup); err != nil {
			return restorePlace{}, deploy.BackupDestination{}, err
		}
	}
	places := restorePlaces(b, own, svc.Backup.CopyTo)
	if len(places) == 0 {
		return restorePlace{}, deploy.BackupDestination{}, fmt.Errorf("backup %s can't be restored: it was deleted by retention: %w", b.ID, store.ErrNotFound)
	}

	chosen := places[0]
	if from != "" {
		i := slices.IndexFunc(places, func(p restorePlace) bool { return p.Destination == from })
		if i < 0 {
			return restorePlace{}, deploy.BackupDestination{}, app.NewFieldError("from", "this backup has no copy in %q; it is in %s", from, namesOf(places))
		}
		chosen = places[i]
	} else if len(places) > 1 {
		var err error
		if chosen, err = s.locateBackup(ctx, appID, b, own, places); err != nil {
			return restorePlace{}, deploy.BackupDestination{}, err
		}
	}
	dest, ok, err := s.destinationNamed(ctx, chosen.Destination)
	if err != nil {
		return restorePlace{}, deploy.BackupDestination{}, err
	}
	if !ok {
		return restorePlace{}, deploy.BackupDestination{}, fmt.Errorf("%w: %q was removed", ErrNoBackupDestination, chosen.Destination)
	}
	return chosen, dest, nil
}

// locateBackup asks, with a check job, which of the places still holds the
// backup, in order, and returns the first that does. A place that says the object
// isn't there is marked expired in its record. Places whose destination was
// removed can't be asked and are left out.
func (s *Service) locateBackup(ctx context.Context, appID string, b store.BackupRecord, own string, places []restorePlace) (restorePlace, error) {
	var asked []restorePlace
	var checks []deploy.CheckPlace
	for _, p := range places {
		dest, ok, err := s.destinationNamed(ctx, p.Destination)
		if err != nil {
			return restorePlace{}, err
		}
		if ok {
			asked = append(asked, p)
			checks = append(checks, deploy.CheckPlace{Dest: dest, Path: p.Object})
		}
	}
	switch len(asked) {
	case 0:
		return restorePlace{}, fmt.Errorf("%w: %s was removed", ErrNoBackupDestination, namesOf(places))
	case 1:
		return asked[0], nil
	}
	res, err := s.deployer.CheckBackupPlaces(ctx, nomadjob.CheckModeLocate, checks)
	if err != nil {
		return restorePlace{}, err
	}
	for _, n := range res.Missing {
		p := asked[n-1]
		if p.Destination == own {
			err = s.store.ExpireBackups(ctx, appID, []string{b.ID})
		} else {
			err = s.store.ExpireBackupCopies(ctx, appID, p.Destination, []string{b.ID})
		}
		if err != nil {
			s.log.Warn("mark a backup missing from a destination", "app", appID, "backup", b.ID, "destination", p.Destination, "error", err)
		}
	}
	if len(res.Missing) > 0 {
		s.backupsChanged(appID)
	}
	if res.Found > 0 {
		return asked[res.Found-1], nil
	}
	if len(res.Unreachable) > 0 {
		names := make([]restorePlace, len(res.Unreachable))
		for i, n := range res.Unreachable {
			names[i] = asked[n-1]
		}
		return restorePlace{}, fmt.Errorf("%w: the backup wasn't found where it could be looked for, and %s couldn't be reached",
			deploy.ErrNomadUnavailable, namesOf(names))
	}
	return restorePlace{}, fmt.Errorf("%w: it isn't in %s any more", ErrBackupMissing, namesOf(asked))
}

// TestBackupDestination checks the destination called name from the servers: a
// check job lists its folder, writes a small object and deletes it. The outcome
// is kept on the destination and returned; a destination that didn't pass is an
// outcome, not an error. An unknown name is ErrDestinationNotFound, and Nomad
// failing to run the check is Nomad's error.
func (s *Service) TestBackupDestination(ctx context.Context, name string) (store.DestinationTest, error) {
	d, err := s.store.GetBackupDestinationNamed(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return store.DestinationTest{}, fmt.Errorf("%w: %q", ErrDestinationNotFound, name)
	}
	if err != nil {
		return store.DestinationTest{}, err
	}
	dest, err := s.withSecret(ctx, d)
	if err != nil {
		return store.DestinationTest{}, err
	}
	res, err := s.deployer.CheckBackupPlaces(ctx, nomadjob.CheckModeTest, []deploy.CheckPlace{{Dest: dest, Path: deploy.DestinationBase(dest)}})
	if err != nil {
		return store.DestinationTest{}, err
	}
	out := store.DestinationTest{OK: res.OK, TestedAt: time.Now().UTC()}
	switch {
	case res.OK:
		out.Message = "listed, wrote and removed a test object"
	case res.Failed != "":
		out.Message = "could not " + res.Failed + ": " + res.Message
	default:
		out.Message = "could not run the check: " + res.Message
	}
	out.Message = strings.ReplaceAll(out.Message, dest.SecretAccessKey, "***")
	if err := s.store.SaveBackupDestinationTest(ctx, name, out); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.DestinationTest{}, fmt.Errorf("%w: %q", ErrDestinationNotFound, name)
		}
		return store.DestinationTest{}, err
	}
	return out, nil
}
