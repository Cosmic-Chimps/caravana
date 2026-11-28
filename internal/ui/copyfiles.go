package ui

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The files of a copy (feature 2g4z): browse the folders Nomad keeps for a copy,
// read a text file and download any file. Everything goes through the service
// (ListCopyFiles, StatCopyPath, CopyFileText, OpenCopyFile). It is plain HTML:
// every folder and file is a link, so the page needs no script. A file's content
// is only drawn in the page that shows it or streamed by the download; it is
// never put in a notice, a log line or an address, and every response is
// Cache-Control: no-store. A task's secrets and private folders are never
// listed or read.

// filesHref is the page of a copy's files at path ("" is the root).
func filesHref(appID, copyID, path string) string {
	h := "/apps/" + url.PathEscape(appID) + "/copies/" + url.PathEscape(copyID) + "/files"
	if path != "" {
		h += "?path=" + url.QueryEscape(path)
	}
	return h
}

// fileDownloadHref is the download of the file at path.
func fileDownloadHref(appID, copyID, path string) string {
	return "/apps/" + url.PathEscape(appID) + "/copies/" + url.PathEscape(copyID) + "/files/download?path=" + url.QueryEscape(path)
}

// fileCrumb is one part of the folder path above the list: the root named after
// the copy, then each folder, the last one plain.
type fileCrumb struct {
	Label, Href string
	Current     bool
}

// fileRow is one entry of a folder.
type fileRow struct {
	Name     string
	Dir      bool
	Href     string // the folder, or the file's own page
	Size     string // "" for a folder
	Note     string // what a link or a special file is; such a row has no Href
	Clock    clockTime
	HasClock bool
	Download string // "" for a folder
}

// fileOpen is the file whose page it is.
type fileOpen struct {
	Name     string
	Size     string
	Clock    clockTime
	HasClock bool
	Download string
	Text     string
	Shown    bool
	// Note says why the text isn't shown, in a sentence.
	Note string
}

// filesView is what the files page draws.
type filesView struct {
	AppID   string
	Name    string   // the app's name, for the browser's title and the breadcrumb
	Heading sentence // the app by its place label
	Service string   // "" when a refusal came before the copy was found
	CopyID  string
	Short   string
	Back    string // the Running tab
	Crumbs  []fileCrumb
	Folder  bool
	Rows    []fileRow
	File    *fileOpen
	// Refusal is drawn in place of the panel, in the page, as Run a command's are.
	Refusal *filesRefused
}

// filesRefused is why the page can't show what was asked: a heading sentence
// and what to do.
type filesRefused struct {
	Head, Text string
}

// filesPage is the whole files page.
type filesPage struct {
	pageData
	filesView
}

// showCopyFiles answers GET /apps/{id}/copies/{copyId}/files?path=: a folder
// drawn as a list, or a file drawn as its text with a download link. A refusal
// is a plain page with the status the API gives.
func (s *server) showCopyFiles(w http.ResponseWriter, r *http.Request, session string) {
	id, copyID, path := r.PathValue("id"), r.PathValue("copyId"), r.URL.Query().Get("path")
	w.Header().Set("Cache-Control", "no-store")
	v, ok := s.filesHead(w, r, id, copyID)
	if !ok {
		return
	}
	st, err := s.svc.StatCopyPath(r.Context(), id, copyID, path)
	if err != nil {
		s.filesRefusal(w, r, v, err)
		return
	}
	v.Service = st.Service
	now := s.now()
	clean := ""
	if st.Info.Dir {
		folder, err := s.svc.ListCopyFiles(r.Context(), id, copyID, path)
		if err != nil {
			s.filesRefusal(w, r, v, err)
			return
		}
		clean, v.Folder = folder.Path, true
		for _, e := range folder.Entries {
			row := fileRow{Name: e.Name, Dir: e.Dir}
			switch e.Kind() {
			case service.KindFolder:
				row.Href = filesHref(id, copyID, e.Path)
			case service.KindFile:
				row.Href = filesHref(id, copyID, e.Path)
				row.Size = humanBytes(e.Size)
				row.Download = fileDownloadHref(id, copyID, e.Path)
			case service.KindLink:
				row.Note = "link, not followed"
			default:
				row.Note = "pipe or socket, not opened"
			}
			if !e.ModTime.IsZero() {
				row.Clock, row.HasClock = clockOf(now, e.ModTime), true
			}
			v.Rows = append(v.Rows, row)
		}
	} else {
		view, err := s.svc.CopyFileText(r.Context(), id, copyID, path)
		if err != nil {
			s.filesRefusal(w, r, v, err)
			return
		}
		clean = view.Info.Path
		o := &fileOpen{Name: view.Info.Name, Size: humanBytes(view.Info.Size), Download: fileDownloadHref(id, copyID, clean),
			Text: view.Text, Shown: view.Shown}
		if !view.Info.ModTime.IsZero() {
			o.Clock, o.HasClock = clockOf(now, view.Info.ModTime), true
		}
		switch view.Reason {
		case service.ViewTooLarge:
			o.Note = "It is larger than 512 KB: download it to read it."
		case service.ViewNotText:
			o.Note = "It isn't text: download it."
		}
		v.File = o
	}
	v.Crumbs = fileCrumbs(id, copyID, v.Short, clean)
	s.renderFiles(w, r, http.StatusOK, v)
}

// filesHead reads the app and starts the page's view: the app by its place
// label, the copy, the way back. An unknown app is the plain not-found page
// (ok false, already answered).
func (s *server) filesHead(w http.ResponseWriter, r *http.Request, id, copyID string) (filesView, bool) {
	rec, err := s.svc.GetApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return filesView{}, false
	}
	if err != nil {
		s.serverError(w, r, "read app", err)
		return filesView{}, false
	}
	v := filesView{AppID: id, Name: appName(id, rec.Place), CopyID: copyID, Short: shortID(copyID), Back: "/apps/" + url.PathEscape(id),
		Heading: s.appTitle(r.Context(), id, rec.Place)}
	return v, true
}

// appName is the app's name, its ID for an app stored without one.
func appName(id string, place app.Place) string {
	if place.Name != "" {
		return place.Name
	}
	return id
}

// appTitle is an app named by its place label: its name, with its project and
// environment when there is more than one place.
func (s *server) appTitle(ctx context.Context, id string, place app.Place) sentence {
	pl, err := s.loadPlaces(ctx)
	if err != nil {
		return sentence{mono(id)}
	}
	return placeLabel(id, place, pl)
}

// renderFiles draws the files page with status.
func (s *server) renderFiles(w http.ResponseWriter, r *http.Request, status int, v filesView) {
	head, err := s.shellPage(r, "Files in "+v.Name, navProjects, s.appCrumbs(r.Context(), v.AppID,
		crumb{Label: v.Name, Href: v.Back}, crumb{Label: "Files"})...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	s.render(w, status, "files", filesPage{pageData: head, filesView: v})
}

// fileCrumbs makes the path above a list: the root, named after the copy, and
// each folder down to the open one.
func fileCrumbs(appID, copyID, short, path string) []fileCrumb {
	crumbs := []fileCrumb{{Label: "copy " + short, Href: filesHref(appID, copyID, ""), Current: path == ""}}
	if path == "" {
		return crumbs
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		last := i == len(parts)-1
		c := fileCrumb{Label: p, Current: last}
		if !last {
			c.Href = filesHref(appID, copyID, strings.Join(parts[:i+1], "/"))
		}
		crumbs = append(crumbs, c)
	}
	return crumbs
}

// downloadCopyFile answers GET /apps/{id}/copies/{copyId}/files/download?path=:
// the file's bytes as an attachment, as the API's downloadCopyFile does.
func (s *server) downloadCopyFile(w http.ResponseWriter, r *http.Request, session string) {
	id, copyID := r.PathValue("id"), r.PathValue("copyId")
	w.Header().Set("Cache-Control", "no-store")
	v, ok := s.filesHead(w, r, id, copyID)
	if !ok {
		return
	}
	f, err := s.svc.OpenCopyFile(r.Context(), id, copyID, r.URL.Query().Get("path"))
	if err != nil {
		s.filesRefusal(w, r, v, err)
		return
	}
	err = httpx.Download(w, f.Info.Name, f.Info.Size, func(out io.Writer) error {
		_, err := f.WriteTo(out)
		return err
	})
	if err != nil {
		v.Service = f.Service
		s.filesRefusal(w, r, v, err)
	}
}

// filesRefusal draws why the files can't be shown. A refusal that knows the
// app and the copy (a refused path, nothing at it, a copy not running, a stopped
// app, Nomad down) is drawn inside the files page, with the copy's root to start
// again from and the status the API gives; it is never a redirect. A copy that
// isn't the app's is the plain page. Nothing about a file's content is in it.
func (s *server) filesRefusal(w http.ResponseWriter, r *http.Request, v filesView, err error) {
	var fe *app.FieldError
	status, refused := 0, filesRefused{}
	switch {
	case errors.Is(err, service.ErrNotAllowed):
		s.refuse(w, r, err)
		return
	case errors.As(err, &fe):
		status, refused = http.StatusUnprocessableEntity, filesRefused{"That path can't be shown.", "The " + fe.Field + " " + fe.Message + "."}
	case errors.Is(err, service.ErrFileNotFound):
		status, refused = http.StatusNotFound, filesRefused{"There is nothing at that path.",
			"The copy's folders change while it runs, so a link may be out of date. Start again from the copy's root."}
	case errors.Is(err, service.ErrAppStopped):
		status, refused = http.StatusConflict, filesRefused{"The app is stopped.", appStoppedText}
	case errors.Is(err, service.ErrCopyNotRunning):
		status, refused = http.StatusConflict, filesRefused{"That copy isn't running.",
			"Files can be read only from a copy that is running. The Running tab lists the copies the app has now."}
	case errors.Is(err, store.ErrNotFound):
		s.message(w, r, http.StatusNotFound, "Files", "That copy isn't part of the app.",
			"The Running tab lists the copies the app has now.")
		return
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't read the files of a copy", "app", v.AppID, "error", err)
		text := "The files come from Nomad, and it didn't answer. Try again in a moment."
		if strings.Contains(err.Error(), "read-fs") {
			text = "Nomad refused to read the copy's files: Caravana's policy needs the read-fs capability. Run the installer again to update it."
		}
		status, refused = http.StatusBadGateway, filesRefused{"Nomad can't be reached.", text}
	default:
		s.serverError(w, r, "read the files of a copy", err)
		return
	}
	v.Crumbs = []fileCrumb{{Label: "copy " + v.Short, Href: filesHref(v.AppID, v.CopyID, "")}}
	v.Folder, v.Rows, v.File, v.Refusal = false, nil, nil, &refused
	s.renderFiles(w, r, status, v)
}
