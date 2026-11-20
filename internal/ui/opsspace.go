package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// Reclaiming space in the console (feature 045): the Space section of the server page, the clean-up
// page, the page that deletes a volume no app declares, and the setting. It reads and changes only
// through internal/service (ServerSpace, RequestCleanup, DeleteOrphanVolume, PutCleanupSettings).

// spaceImageLimit is how many unused images the clean-up page lists before it says "and N more".
const spaceImageLimit = 12

// ---- what the section draws --------------------------------------------------------------------

// srvSpace is the Space section of the server page.
type srvSpace struct {
	// PartHref is the section's own refresh route.
	PartHref string
	// Reclaimable is "1.5 GB" in the section's label; empty when there is nothing to reclaim or nothing is known.
	Reclaimable string
	// Flash is what the post that brought the browser here did ("Cleaning up…"), drawn first.
	Flash sentence
	Rows  []spaceRow
	// Why says why the images, Docker's volumes and the cache aren't known (no agent, no inventory yet).
	Why sentence
	// Open is the line of a clean-up under way; Last the newest one that ended.
	Open *spaceRun
	Last *spaceRun
	// CleanHref is "Clean up now…": empty when the server has no agent or a clean-up goes.
	CleanHref string
	// Orphans are the volumes no app declares, on this server. OrphansUnread says Nomad couldn't be read.
	Orphans       []spaceOrphan
	OrphansUnread bool
	Settings      spaceSettings
}

// spaceRow is one kind of thing a server holds: unused images, Docker's leftover volumes, the cache.
type spaceRow struct {
	Label string
	// Value is "4 · 1.1 GB", "none" or "—" (None).
	Value string
	None  bool
	Note  string
}

// spaceRun is a clean-up as a line: the lead ("Cleaned up"), when, and the rest of the sentence.
type spaceRun struct {
	Mark string
	Lead string
	When *when
	Rest string
}

// spaceOrphan is one volume no app declares.
type spaceOrphan struct {
	Name string
	// From says whose it was: "from app shop", "from app web (lab › staging)" or "its app was deleted".
	From sentence
	// Size is "2.3 GB", or "not measured yet".
	Size string
	// Last is "last deployed with it Oct 12"; empty when unknown.
	Last  string
	InUse bool
	// DeleteHref is the page that deletes it; empty while a copy mounts it.
	DeleteHref string
}

// spaceSettings is the settings form.
type spaceSettings struct {
	Action    string
	Server    string
	Automatic bool
	Keeps     []spaceKeep
}

// spaceKeep is one option of the keep select.
type spaceKeep struct {
	N        int
	Selected bool
}

// ---- building the section ----------------------------------------------------------------------

// spaceOf reads the server's space and builds the section. query carries the notices of the post that
// redirected here (?cleaning, ?deleted, ?deleting, ?saved); each is drawn only while it is still true.
// ErrServerNotFound is returned as it is.
func (s *server) spaceOf(r *http.Request, name string, query url.Values) (*srvSpace, error) {
	ctx := r.Context()
	sp, err := s.svc.ServerSpace(ctx, name)
	if err != nil {
		return nil, err
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := &srvSpace{PartHref: "/parts/ops/servers/" + url.PathEscape(name) + "/space", OrphansUnread: sp.OrphansUnavailable}
	if sp.ReclaimableBytes > 0 {
		out.Reclaimable = humanBytes(sp.ReclaimableBytes)
	}

	unused := spaceRow{Label: "Unused images", Value: "—", None: true}
	docker := spaceRow{Label: "Docker's leftover volumes", Value: "—", None: true}
	cache := spaceRow{Label: "Build cache", Value: "—", None: true}
	if im := sp.Images; im != nil {
		unused.None = false
		unused.Value = "none"
		if im.UnusedCount > 0 {
			unused.Value = strconv.Itoa(im.UnusedCount) + " · " + humanBytes(im.UnusedBytes)
		}
		unused.Note = "of " + plural(im.Count, "image") + ", " + humanBytes(im.Bytes) + " in all"
		if im.Truncated {
			unused.Note += " (the agent named only the largest 1000)"
		}
	}
	if sp.DockerVolumes != nil {
		docker.None, docker.Value = false, "none"
		if sp.DockerVolumes.Count > 0 {
			docker.Value = strconv.FormatInt(sp.DockerVolumes.Count, 10) + " · " + humanBytes(sp.DockerVolumes.Bytes)
		}
		docker.Note = "volumes of removed containers, not the apps'"
	}
	if sp.BuildCacheBytes != nil {
		cache.None, cache.Value = false, "none"
		if *sp.BuildCacheBytes > 0 {
			cache.Value = humanBytes(*sp.BuildCacheBytes)
		}
	}
	out.Rows = []spaceRow{unused, docker, cache}
	if sp.Images == nil {
		out.Why = plainS(imagesWhy(sp.ImagesUnavailable))
	}

	if c := sp.OpenCleanup; c != nil {
		out.Open = &spaceRun{Mark: "rolling", Lead: "Cleaning up…"}
		if c.Status == store.CleanupRequested {
			out.Open.Rest = "waiting for the server's agent to pick it up"
		}
	}
	if c := sp.LastCleanup; c != nil {
		out.Last = cleanupRun(now, *c)
	}
	if sp.Images != nil && sp.OpenCleanup == nil {
		out.CleanHref = serverHref(name) + "/clean"
	}

	for _, o := range sp.Orphans {
		out.Orphans = append(out.Orphans, orphanRow(now, pl, name, o))
	}

	out.Settings = spaceSettings{Action: "/ops/cleanup/settings", Server: name, Automatic: sp.Settings.Automatic}
	for n := 1; n <= 10; n++ {
		out.Settings.Keeps = append(out.Settings.Keeps, spaceKeep{N: n, Selected: n == sp.Settings.KeepDeployments})
	}

	out.Flash = s.spaceFlash(r, query, sp)
	return out, nil
}

// The words the service gives for images it can't read (service.SpaceView.ImagesUnavailable).
const (
	imagesNoAgentWord     = "no agent"
	imagesNoInventoryWord = "no inventory yet"
)

// errNoInventory is the refusal of a clean-up on a server whose agent hasn't reported its images yet.
var errNoInventory = errors.New("the agent hasn't reported its images yet")

// imagesWhy is why a server's images aren't known, in the console's words.
func imagesWhy(reason string) string {
	switch reason {
	case imagesNoAgentWord:
		return "This server has no agent reporting its images: run the installer on it."
	case imagesNoInventoryWord:
		return "The agent hasn't reported its images yet."
	}
	return reason
}

// cleanupRun is a clean-up that ended as a line.
func cleanupRun(now time.Time, c store.ImageCleanup) *spaceRun {
	at := c.FinishedAt
	if at.IsZero() {
		at = c.RequestedAt
	}
	w := whenOf(now, &at, "")
	by := "by you"
	if c.Trigger == store.CleanupBySchedule {
		by = "on schedule"
	}
	run := &spaceRun{When: &w}
	switch c.Status {
	case store.CleanupDone:
		run.Mark, run.Lead = "healthy", "Cleaned up"
		rest := " (" + by + "): removed " + plural(c.ImagesRemoved, "image") + ", freed " + humanBytes(c.BytesFreed)
		if c.VolumesRemoved > 0 {
			rest += ", removed " + plural(c.VolumesRemoved, "Docker volume")
		}
		run.Rest = rest + "."
	case store.CleanupLost:
		run.Mark, run.Lead = "warning", "A clean-up never reported back"
		run.Rest = " (" + by + ")."
		if c.Error != "" {
			run.Rest = " (" + by + "): " + sentenceEnd(c.Error)
		}
	default:
		run.Mark, run.Lead = "failed", "A clean-up failed"
		run.Rest = " (" + by + ")."
		if c.Error != "" {
			run.Rest = " (" + by + "): " + sentenceEnd(c.Error)
		}
	}
	return run
}

// sentenceEnd ends a text that is a sentence with a full stop.
func sentenceEnd(text string) string {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasSuffix(text, ".") {
		return text
	}
	return text + "."
}

// orphanRow is one orphan volume as a row.
func orphanRow(now time.Time, pl places, server string, o service.OrphanVolume) spaceOrphan {
	row := spaceOrphan{Name: o.Name, InUse: o.InUse, Size: "not measured yet"}
	switch {
	case o.App != "" && o.AppExists:
		row.From = join(plainS("from app "), stayWho(pl, o.App, ""))
	case o.App != "":
		row.From = plainS("its app was deleted")
	}
	if o.Bytes != nil {
		row.Size = humanBytes(*o.Bytes)
	}
	if !o.LastDeployedAt.IsZero() {
		row.Last = "last deployed with it " + shortDate(now, o.LastDeployedAt)
	}
	if !o.InUse {
		row.DeleteHref = volumeDeleteHref(server, o.Name)
	}
	return row
}

// volumeDeleteHref is the page that deletes a volume that sits on a server.
func volumeDeleteHref(server, volume string) string {
	return serverHref(server) + "/volumes/" + url.PathEscape(volume) + "/delete"
}

// spaceFlash is what the redirect after a post says: a clean-up requested, a volume deleted, settings saved. Each
// is drawn only while it is still true, so a stale address never claims something that has changed.
func (s *server) spaceFlash(r *http.Request, q url.Values, sp service.SpaceView) sentence {
	switch {
	case q.Get("cleaning") != "":
		if c := sp.OpenCleanup; c != nil && c.ID == q.Get("cleaning") {
			return plainS("Cleaning up. The server's agent picks it up within a minute, and this section shows how it went.")
		}
	case q.Get("deleted") != "":
		name := q.Get("deleted")
		if !sp.OrphansUnavailable && !hasOrphan(sp, name) {
			return sentence{plain("Deleted the volume "), mono(name), plain(".")}
		}
	case q.Get("deleting") != "":
		name := q.Get("volume")
		if d, err := s.svc.GetVolumeDeletion(r.Context(), name, q.Get("deleting")); err == nil &&
			(d.Status == store.DeletionArchiving || d.Status == store.DeletionDeleting) {
			return sentence{plain("Archiving "), mono(name), plain(" to the backup destination, then deleting it.")}
		}
	case q.Get("saved") == "1":
		return plainS("Saved.")
	}
	return nil
}

func hasOrphan(sp service.SpaceView, name string) bool {
	for _, o := range sp.Orphans {
		if o.Name == name {
			return true
		}
	}
	return false
}

// partServerSub answers GET /parts/ops/servers/{kind}/{rest}. Two parts live under that shape and Go's router
// can't tell /parts/ops/servers/joins/{id} from /parts/ops/servers/{name}/space (both match
// /parts/ops/servers/joins/space, and neither is the more specific), so one pattern takes both and this
// dispatches, as showServerSub does for the pages.
func (s *server) partServerSub(w http.ResponseWriter, r *http.Request, session string) {
	kind, rest := r.PathValue("kind"), r.PathValue("rest")
	switch {
	case kind == "joins":
		r.SetPathValue("id", rest)
		s.partServerJoin(w, r, session)
	case rest == "space":
		s.partOpsServerSpace(w, r, kind)
	default:
		http.NotFound(w, r)
	}
}

// partOpsServerSpace answers GET /parts/ops/servers/{name}/space: the section alone, drawn again on the
// servers notice (the server's sync, and the end of a clean-up, publish it).
func (s *server) partOpsServerSpace(w http.ResponseWriter, r *http.Request, name string) {
	sp, err := s.spaceOf(r, name, nil)
	if errors.Is(err, service.ErrServerNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("draw the space section", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.renderPart(w, "opsserver", "srv-space", sp)
}

// ---- the clean-up page --------------------------------------------------------------------------

// cleanServerPage is the confirmation page of a clean-up. Refusal, when set, takes the panel's and the
// button's place.
type cleanServerPage struct {
	pageData
	Name     string
	Action   string
	Back     string
	Headline sentence
	Refusal  sentence
	// Unused lists the largest unused images; More is how many more there are. Total is "4 images, 1.1 GB".
	Unused []cleanImage
	More   int
	Total  string
	// Cache is the build cache's size, "" when there is none.
	Cache string
	// Keep is how many deployments of each app keep their images.
	Keep int
	// Volumes is the box "Also remove Docker's leftover volumes (154, 91 MB)"; empty when Docker has none.
	Volumes string
	Checked bool
}

// cleanImage is one image the clean-up would remove.
type cleanImage struct {
	Name, Size string
}

// showCleanServer answers GET /ops/servers/{name}/clean (through showServerSub).
func (s *server) showCleanServer(w http.ResponseWriter, r *http.Request, name string) {
	s.drawCleanServer(w, r, name, nil, false)
}

// cleanServer answers POST /ops/servers/{name}/clean: a clean-up is requested and the browser goes to the
// server's page (303). A refusal draws the confirmation again with the service's sentence, never a redirect.
func (s *server) cleanServer(w http.ResponseWriter, r *http.Request, session string) {
	name := r.PathValue("name")
	if !s.readForm(w, r) {
		return
	}
	dockerVolumes := r.PostForm.Get("dockerVolumes") != ""
	c, err := s.svc.RequestCleanup(r.Context(), name, store.CleanupByOwner, dockerVolumes)
	if err != nil {
		s.drawCleanServer(w, r, name, err, dockerVolumes)
		return
	}
	http.Redirect(w, r, serverHref(name)+"?cleaning="+url.QueryEscape(c.ID)+"#space", http.StatusSeeOther)
}

// cleanRefusalStatus is the status of a refused clean-up: the owner's state (409), an unknown server (404).
func cleanRefusalStatus(err error) int {
	switch {
	case errors.Is(err, service.ErrServerNotFound):
		return http.StatusNotFound
	case errors.Is(err, service.ErrNoAgent), errors.Is(err, service.ErrCleanupRunning), errors.Is(err, service.ErrKeepListTooLong),
		errors.Is(err, errNoInventory):
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// drawCleanServer draws the confirmation. refused is why a post was turned down; with none, the page reads
// the space and refuses for the same reasons the service would.
func (s *server) drawCleanServer(w http.ResponseWriter, r *http.Request, name string, refused error, dockerVolumes bool) {
	ctx := r.Context()
	sp, err := s.svc.ServerSpace(ctx, name)
	if errors.Is(err, service.ErrServerNotFound) {
		s.message(w, r, http.StatusNotFound, "Server not found", "No such server.", "There is no server called "+name+".")
		return
	}
	if err != nil {
		s.serverError(w, r, "draw the clean-up", err)
		return
	}
	page := cleanServerPage{Name: sp.Server, Action: serverHref(sp.Server) + "/clean", Back: serverHref(sp.Server) + "#space",
		Headline: sentence{plain("Clean up "), mono(sp.Server), plain("?")}, Keep: sp.Settings.KeepDeployments, Checked: dockerVolumes}
	status := http.StatusOK
	if refused == nil {
		switch {
		case sp.Images != nil:
			if sp.OpenCleanup != nil {
				refused = &service.CleanupRunningError{Server: sp.Server}
			}
		case sp.ImagesUnavailable == imagesNoInventoryWord:
			refused = errNoInventory
		case sp.ImagesUnavailable == imagesNoAgentWord || sp.ImagesUnavailable == "":
			refused = &service.NoAgentError{Server: sp.Server}
		default:
			refused = service.ErrKeepListTooLong // the only other reason the images are unread
		}
	}
	if refused != nil {
		status = cleanRefusalStatus(refused)
		if status == http.StatusInternalServerError {
			s.serverError(w, r, "request a clean-up", refused)
			return
		}
		page.Refusal = plainS(refusalWords(refused))
	} else {
		im := sp.Images
		for i, img := range im.Unused {
			if i == spaceImageLimit {
				page.More = len(im.Unused) - spaceImageLimit
				break
			}
			page.Unused = append(page.Unused, cleanImage{Name: spaceImageName(img), Size: humanBytes(img.Bytes)})
		}
		page.Total = plural(im.UnusedCount, "image") + ", " + humanBytes(im.UnusedBytes)
		if sp.BuildCacheBytes != nil && *sp.BuildCacheBytes > 0 {
			page.Cache = humanBytes(*sp.BuildCacheBytes)
		}
		if dv := sp.DockerVolumes; dv != nil && dv.Count > 0 {
			page.Volumes = strconv.FormatInt(dv.Count, 10) + ", " + humanBytes(dv.Bytes)
		}
	}
	head, err := s.shellPage(r, "Clean up "+sp.Server, navOpsServer, crumb{Label: "Servers", Href: "/ops/servers"},
		crumb{Label: sp.Server, Href: serverHref(sp.Server)}, crumb{Label: "Clean up"})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "opsclean", page)
}

// spaceImageName is how an image is called on the clean-up page: its first tag, else its short ID.
func spaceImageName(img service.SpaceImage) string {
	if len(img.Tags) > 0 {
		return img.Tags[0]
	}
	return strings.TrimPrefix(img.ID, "sha256:")[:min(12, len(strings.TrimPrefix(img.ID, "sha256:")))]
}

// ---- the page that deletes a volume ------------------------------------------------------------

// deleteVolumePage is the confirmation page that deletes a volume no app declares. Refusal, when set, takes
// the form's place.
type deleteVolumePage struct {
	pageData
	Server, Volume string
	Action, Back   string
	Headline       sentence
	Refusal        sentence
	// Facts is "2.3 GB · from app shop · last deployed with it Oct 12".
	Facts sentence
	// Typed is what was typed in the confirmation, with Error when it isn't the volume's name.
	Typed string
	Error sentence
	// CanBackUp shows the box that archives the volume before deleting it; BackUp is whether it is ticked.
	CanBackUp bool
	BackUp    bool
}

// showDeleteVolume answers GET /ops/servers/{name}/volumes/{volume}/delete.
func (s *server) showDeleteVolume(w http.ResponseWriter, r *http.Request, session string) {
	s.drawDeleteVolume(w, r, r.PathValue("name"), r.PathValue("volume"), nil, "", false)
}

// deleteVolume answers POST /ops/servers/{name}/volumes/{volume}/delete: the volume is deleted and the
// browser goes to the server's page (303). A wrong name or a refusal draws the page again with its sentence.
func (s *server) deleteVolume(w http.ResponseWriter, r *http.Request, session string) {
	name, volume := r.PathValue("name"), r.PathValue("volume")
	if !s.readForm(w, r) {
		return
	}
	typed := strings.TrimSpace(r.PostForm.Get("confirm"))
	backUp := r.PostForm.Get("backUp") != ""
	d, err := s.svc.DeleteOrphanVolume(r.Context(), volume, typed, backUp, app.TriggerConsole)
	if err != nil {
		s.drawDeleteVolume(w, r, name, volume, err, typed, backUp)
		return
	}
	target := serverHref(name) + "?deleted=" + url.QueryEscape(volume)
	if d.Status == store.DeletionArchiving {
		target = serverHref(name) + "?deleting=" + url.QueryEscape(d.ID) + "&volume=" + url.QueryEscape(volume)
	}
	http.Redirect(w, r, target+"#space", http.StatusSeeOther)
}

// deleteRefusal is a refused deletion as its status and the sentence the page says.
func deleteRefusal(err error, server, volume string) (int, sentence) {
	var field *app.FieldError
	var notOrphan *service.VolumeNotOrphanError
	switch {
	case errors.As(err, &field) && field.Field == "confirm":
		return http.StatusUnprocessableEntity, sentence{plain("Type "), mono(volume), plain(" to confirm.")}
	case errors.Is(err, service.ErrVolumeNotFound):
		return http.StatusNotFound, sentence{plain("Nomad has no volume called "), mono(volume), plain(". It may be deleted already.")}
	case errors.As(err, &notOrphan):
		return http.StatusConflict, plainS(notOrphan.Error())
	case errors.Is(err, service.ErrVolumeInUse):
		return http.StatusConflict, plainS("A running copy mounts this volume. It can be deleted once nothing uses it.")
	case errors.Is(err, service.ErrDeletionRunning):
		return http.StatusConflict, plainS("A deletion of this volume is already going.")
	case errors.Is(err, service.ErrNoBackupDestination):
		return http.StatusConflict, plainS("No backup destination is set: add one under Settings › Backup storage, or delete without backing up.")
	case errors.Is(err, deploy.ErrNomadUnavailable):
		return http.StatusBadGateway, plainS(refusalWords(err))
	}
	return http.StatusInternalServerError, nil
}

// drawDeleteVolume draws the page. refused is why a post was turned down; with none, the page looks for the
// volume among the server's orphans and refuses when it is mounted.
func (s *server) drawDeleteVolume(w http.ResponseWriter, r *http.Request, server, volume string, refused error, typed string, backUp bool) {
	ctx := r.Context()
	sp, err := s.svc.ServerSpace(ctx, server)
	if errors.Is(err, service.ErrServerNotFound) {
		s.message(w, r, http.StatusNotFound, "Server not found", "No such server.", "There is no server called "+server+".")
		return
	}
	if err != nil {
		s.serverError(w, r, "draw the volume deletion", err)
		return
	}
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		s.serverError(w, r, "read projects", err)
		return
	}
	page := deleteVolumePage{Server: sp.Server, Volume: volume, Typed: typed, BackUp: backUp,
		Action: volumeDeleteHref(sp.Server, volume), Back: serverHref(sp.Server) + "#space",
		Headline: sentence{plain("Delete "), mono(volume), plain("?")}}
	status := http.StatusOK
	var orphan *service.OrphanVolume
	for i := range sp.Orphans {
		if sp.Orphans[i].Name == volume {
			orphan = &sp.Orphans[i]
		}
	}
	switch {
	case refused != nil:
		var st int
		var words sentence
		if st, words = deleteRefusal(refused, sp.Server, volume); words == nil {
			s.serverError(w, r, "delete a volume", refused)
			return
		}
		status = st
		if st == http.StatusUnprocessableEntity {
			page.Error = words // the typed name is asked for again, the page stays as it was
		} else {
			page.Refusal = words
		}
	case orphan == nil:
		status = http.StatusNotFound
		page.Refusal = sentence{plain("No volume called "), mono(volume), plain(" is waiting on "), mono(sp.Server), plain(". It may be deleted already, or an app declares it.")}
	case orphan.InUse:
		status = http.StatusConflict
		page.Refusal = plainS("A running copy mounts this volume. It can be deleted once nothing uses it.")
	}
	if page.Refusal == nil {
		if orphan != nil {
			row := orphanRow(s.now().UTC(), pl, sp.Server, *orphan)
			page.Facts = join(plainS(row.Size), plainS(" · "), row.From)
			if len(row.From) == 0 {
				page.Facts = plainS(row.Size)
			}
			if row.Last != "" {
				page.Facts = join(page.Facts, plainS(" · "+row.Last))
			}
		}
		_, derr := s.svc.GetBackupDestination(ctx)
		page.CanBackUp = derr == nil
		if derr != nil && !errors.Is(derr, store.ErrNotFound) {
			s.serverError(w, r, "read the backup destination", derr)
			return
		}
	}
	head, err := s.shellPage(r, "Delete "+volume, navOpsServer, crumb{Label: "Servers", Href: "/ops/servers"},
		crumb{Label: sp.Server, Href: serverHref(sp.Server)}, crumb{Label: "Delete " + volume})
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	page.pageData = head
	s.render(w, status, "opsvolumedelete", page)
}

// ---- the setting --------------------------------------------------------------------------------

// saveCleanupSettings answers POST /ops/cleanup/settings: the daily clean-up on or off and how many
// deployments of each app keep their images. The browser goes back to the server page it came from (303).
func (s *server) saveCleanupSettings(w http.ResponseWriter, r *http.Request, session string) {
	if !s.readForm(w, r) {
		return
	}
	keep, err := strconv.Atoi(strings.TrimSpace(r.PostForm.Get("keep")))
	if err != nil {
		s.message(w, r, http.StatusUnprocessableEntity, "Not saved", "That setting wasn't saved.", "Choose a number from 1 to 10 deployments to keep.")
		return
	}
	if _, err := s.svc.PutCleanupSettings(r.Context(), r.PostForm.Get("automatic") != "", keep); err != nil {
		var field *app.FieldError
		if errors.As(err, &field) {
			s.message(w, r, http.StatusUnprocessableEntity, "Not saved", "That setting wasn't saved.", refusalWords(errors.New(field.Message)))
			return
		}
		s.serverError(w, r, "save the clean-up settings", err)
		return
	}
	target := "/ops/servers"
	if name := strings.TrimSpace(r.PostForm.Get("server")); name != "" {
		target = serverHref(name) + "?saved=1#space"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
