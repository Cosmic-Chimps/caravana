package ui

import (
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
	Service string
	CopyID  string
	Short   string
	Back    string // the Running tab
	Crumbs  []fileCrumb
	Folder  bool
	Rows    []fileRow
	File    *fileOpen
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
	if _, err := s.svc.GetApp(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		s.appNotFound(w, r, id)
		return
	} else if err != nil {
		s.serverError(w, r, "read app", err)
		return
	}
	st, err := s.svc.StatCopyPath(r.Context(), id, copyID, path)
	if err != nil {
		s.filesRefusal(w, r, id, err)
		return
	}
	v := filesView{AppID: id, Service: st.Service, CopyID: copyID, Short: shortID(copyID), Back: "/apps/" + url.PathEscape(id)}
	now := s.now()
	clean := ""
	if st.Info.Dir {
		folder, err := s.svc.ListCopyFiles(r.Context(), id, copyID, path)
		if err != nil {
			s.filesRefusal(w, r, id, err)
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
			s.filesRefusal(w, r, id, err)
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

	title := "Files in " + id
	head, err := s.shellPage(r, title, navProjects, s.appCrumbs(r.Context(), id,
		crumb{Label: id, Href: v.Back}, crumb{Label: "Files"})...)
	if err != nil {
		s.serverError(w, r, "read admin", err)
		return
	}
	s.render(w, http.StatusOK, "files", filesPage{pageData: head, filesView: v})
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
	f, err := s.svc.OpenCopyFile(r.Context(), id, copyID, r.URL.Query().Get("path"))
	if err != nil {
		s.filesRefusal(w, r, id, err)
		return
	}
	err = httpx.Download(w, f.Info.Name, f.Info.Size, func(out io.Writer) error {
		_, err := f.WriteTo(out)
		return err
	})
	if err != nil {
		s.filesRefusal(w, r, id, err)
	}
}

// filesRefusal draws why the files can't be shown, as a plain page with the
// status the API gives. Nothing about the content is in it.
func (s *server) filesRefusal(w http.ResponseWriter, r *http.Request, appID string, err error) {
	const title = "Files"
	var fe *app.FieldError
	switch {
	case errors.Is(err, service.ErrNotAllowed):
		s.refuse(w, r, err)
	case errors.As(err, &fe):
		s.message(w, r, http.StatusUnprocessableEntity, title, "That path can't be shown.", "The "+fe.Field+" "+fe.Message+".")
	case errors.Is(err, service.ErrFileNotFound):
		s.message(w, r, http.StatusNotFound, title, "There is nothing at that path.",
			"The copy's folders change while it runs, so a link may be out of date. Open the files of the copy again from the Running tab.")
	case errors.Is(err, service.ErrAppStopped):
		s.message(w, r, http.StatusConflict, title, "The app is stopped.", appStoppedText)
	case errors.Is(err, service.ErrCopyNotRunning):
		s.message(w, r, http.StatusConflict, title, "That copy isn't running.",
			"Files can be read only from a copy that is running. The Running tab lists the copies the app has now.")
	case errors.Is(err, store.ErrNotFound):
		s.message(w, r, http.StatusNotFound, title, "That copy isn't part of the app.",
			"The Running tab lists the copies the app has now.")
	case errors.Is(err, deploy.ErrNomadUnavailable), errors.Is(err, deploy.ErrNomadRejected):
		s.log.Warn("can't read the files of a copy", "app", appID, "error", err)
		text := "The files come from Nomad, and it didn't answer. Try again in a moment."
		if strings.Contains(err.Error(), "read-fs") {
			text = "Nomad refused to read the copy's files: Caravana's policy needs the read-fs capability. Run the installer again to update it."
		}
		s.message(w, r, http.StatusBadGateway, title, "Nomad can't be reached.", text)
	default:
		s.serverError(w, r, "read the files of a copy", err)
	}
}
