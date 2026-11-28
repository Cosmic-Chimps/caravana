package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/httpx"
	"github.com/cosmic-chimps/caravana/internal/service"
)

// The files of a copy (feature 2g4z): listCopyFiles and downloadCopyFile. Both
// are read only and go through service.ListCopyFiles and service.OpenCopyFile.
// A file's content is only ever the body of the download: every answer, errors
// included, is Cache-Control: no-store, and nothing here logs a name's content
// or puts it in an error. A task's secrets and private folders are refused
// before Nomad is asked.

// codeFileNotFound is a path that isn't in the copy's directory.
const codeFileNotFound = "file_not_found"

// copyFilesError maps what the file reads add to the usual errors: a refused
// path is 422 on "path" (as the logs' field errors are), a path that isn't there
// 404 file_not_found. Others pass through for mapError.
func copyFilesError(err error) error {
	var fe *app.FieldError
	switch {
	case errors.As(err, &fe):
		return &Error{Status: http.StatusUnprocessableEntity, Code: codeInvalidField, Field: fe.Field, Message: fe.Message}
	case errors.Is(err, service.ErrFileNotFound):
		return &Error{Status: http.StatusNotFound, Code: codeFileNotFound, Field: "path", Message: err.Error()}
	}
	return err
}

// copyFileJSON is one entry of a folder (schema CopyFile).
type copyFileJSON struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Size       int64     `json:"size"`
	Mode       string    `json:"mode"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

// copyFolderJSON is what listCopyFiles answers (schema CopyFolder).
type copyFolderJSON struct {
	Path    string         `json:"path"`
	Service string         `json:"service"`
	Entries []copyFileJSON `json:"entries"`
}

func toCopyFolderJSON(f service.CopyFolder) copyFolderJSON {
	out := copyFolderJSON{Path: f.Path, Service: f.Service, Entries: make([]copyFileJSON, 0, len(f.Entries))}
	for _, e := range f.Entries {
		out.Entries = append(out.Entries, copyFileJSON{Name: e.Name, Path: e.Path, Kind: e.Kind(), Size: e.Size,
			Mode: e.Mode, ModifiedAt: e.ModTime.UTC()})
	}
	return out
}

// listCopyFiles answers GET /v1/apps/{id}/copies/{copyId}/files?path=: the
// folder at path of the copy's allocation directory ("" is the root).
func (s *server) listCopyFiles(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	f, err := s.svc.ListCopyFiles(r.Context(), r.PathValue("id"), r.PathValue("copyId"), r.URL.Query().Get("path"))
	if err != nil {
		return copyFilesError(err)
	}
	writeJSON(w, http.StatusOK, toCopyFolderJSON(f))
	return nil
}

// downloadCopyFile answers GET /v1/apps/{id}/copies/{copyId}/files/download?path=:
// the file's bytes as an attachment.
func (s *server) downloadCopyFile(w http.ResponseWriter, r *http.Request) error {
	w.Header().Set("Cache-Control", "no-store")
	f, err := s.svc.OpenCopyFile(r.Context(), r.PathValue("id"), r.PathValue("copyId"), r.URL.Query().Get("path"))
	if err != nil {
		return copyFilesError(err)
	}
	err = httpx.Download(w, f.Info.Name, f.Info.Size, func(out io.Writer) error {
		_, err := f.WriteTo(out)
		return err
	})
	return copyFilesError(err)
}
