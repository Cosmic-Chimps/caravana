package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/cosmic-chimps/caravana/internal/app"
	"github.com/cosmic-chimps/caravana/internal/audit"
	"github.com/cosmic-chimps/caravana/internal/deploy"
	"github.com/cosmic-chimps/caravana/internal/store"
)

// The files of a copy (feature 2g4z): list the folders Nomad keeps for a copy,
// read a text file in the console, download any file. Read only: nothing here
// edits or uploads.
//
// What a file holds passes through these calls and nowhere else: it is never
// logged, put in an error, a notice or a record. The audit entry of a view or a
// download has the copy and the path, never the content. A task's secrets and
// private folders are never listed or read (deploy.CleanFilePath refuses them
// before Nomad is asked). Tests search every answer, the store and the log for
// a secret file's content.

// ErrFileNotFound is a path that isn't in the copy's directory. It is deploy's
// error, re-exported so the API and the console never import deploy.
var ErrFileNotFound = deploy.ErrFileNotFound

// MaxViewBytes is the most of a file the console shows as text. A larger file
// is offered for download.
const MaxViewBytes = 512 << 10

// FileInfo is one entry of a copy's directory. It is deploy's type,
// re-exported.
type FileInfo = deploy.FileInfo

// The kinds of entry FileInfo.Kind gives, re-exported.
const (
	KindFolder = deploy.KindFolder
	KindFile   = deploy.KindFile
	KindLink   = deploy.KindLink
	KindOther  = deploy.KindOther
)

// CopyFolder is a folder of a copy: where it is, which service the copy is of,
// and what is in it (folders first, then files, each by name).
type CopyFolder struct {
	Path    string
	Service string
	Entries []FileInfo
}

// CopyPath is what a path of a copy is: a folder or a file. The root is a
// folder with no information of its own.
type CopyPath struct {
	Service string
	Info    FileInfo
}

// Why a file isn't shown as text in CopyFileView.Reason.
const (
	ViewTooLarge = "too_large" // over MaxViewBytes
	ViewNotText  = "not_text"  // not UTF-8, or has a NUL byte
)

// CopyFileView is a file for the console to show.
type CopyFileView struct {
	Service string
	Info    FileInfo
	// Text is the file's content when Shown. Otherwise Reason says why not.
	Text   string
	Shown  bool
	Reason string
}

// CopyFile is a file about to be downloaded: its info, and WriteTo, which
// streams the content. Nothing has been read when it is made.
type CopyFile struct {
	Service string
	Info    FileInfo

	svc    *Service
	copyID string
	// ctx is the request's: a caller that goes away stops the read. A CopyFile
	// lives as long as that request, so keeping it here is safe.
	ctx context.Context
}

// WriteTo streams the file to w, in chunks. If it fails before the first byte
// nothing has been written. It satisfies io.WriterTo.
func (f *CopyFile) WriteTo(w io.Writer) (int64, error) {
	cw := &countWriter{w: w}
	err := f.svc.deployer.ReadFile(f.ctx, f.copyID, f.Info.Path, f.Info.Size, cw)
	return cw.n, copyFilesError(err)
}

// countWriter counts what passes through it.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// copyFilesError maps deploy's file errors to field errors on "path", which the
// API and the console show; ErrFileNotFound and the rest pass through.
func copyFilesError(err error) error {
	var pe *deploy.FilePathError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &pe):
		return app.NewFieldError("path", "%s", pe.Reason)
	case errors.Is(err, deploy.ErrPathNotAllowed):
		return app.NewFieldError("path", "is in a task's secrets or private folder, which are never shown")
	case errors.Is(err, deploy.ErrNotAFolder):
		return app.NewFieldError("path", "is a file, not a folder")
	case errors.Is(err, deploy.ErrNotAFile):
		return app.NewFieldError("path", "is a folder, not a file")
	case errors.Is(err, deploy.ErrSpecialFile):
		return app.NewFieldError("path", "is a pipe, a socket or a device, which is never opened")
	case errors.Is(err, deploy.ErrPathIsLink):
		return app.NewFieldError("path", "goes through a link, and links aren't followed")
	case errors.Is(err, deploy.ErrCopyNotFound):
		return fmt.Errorf("%v: %w", err, store.ErrNotFound)
	}
	return err
}

// filesCopy finds the running copy of the app and checks the path: an unknown
// app or a copy that isn't the app's is store.ErrNotFound, a stopped app
// ErrAppStopped, a copy that has ended or isn't running ErrCopyNotRunning, a
// bad or refused path a *app.FieldError on "path". It returns the cleaned path.
func (s *Service) filesCopy(ctx context.Context, appID, copyID, path string) (Copy, string, error) {
	clean, err := deploy.CleanFilePath(path)
	if err != nil {
		return Copy{}, "", copyFilesError(err)
	}
	rec, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return Copy{}, "", err
	}
	if rec.StoppedAt != nil {
		return Copy{}, "", fmt.Errorf("app %q: %w", appID, ErrAppStopped)
	}
	copies, err := s.deployer.Copies(ctx, appID)
	if err != nil {
		return Copy{}, "", fmt.Errorf("list copies of %q: %w", appID, err)
	}
	i := slices.IndexFunc(copies, func(c Copy) bool { return c.ID == copyID })
	if i < 0 {
		return Copy{}, "", fmt.Errorf("app %q has no copy %q: %w", appID, copyID, store.ErrNotFound)
	}
	c := copies[i]
	if !c.Reachable() {
		return Copy{}, "", fmt.Errorf("copy %q is %s: %w", copyID, c.Status, ErrCopyNotRunning)
	}
	return c, clean, nil
}

// ListCopyFiles lists the folder at path of the copy ("" is the root of its
// allocation directory). Folders come first, each list by name; a task's
// secrets and private folders are never in it. A path that is a file is a
// field error on "path". Listing is a read and leaves no audit entry.
func (s *Service) ListCopyFiles(ctx context.Context, appID, copyID, path string) (CopyFolder, error) {
	c, path, err := s.filesCopy(ctx, appID, copyID, path)
	if err != nil {
		return CopyFolder{}, err
	}
	entries, err := s.deployer.ListFiles(ctx, c.ID, path)
	if err != nil {
		return CopyFolder{}, copyFilesError(err)
	}
	return CopyFolder{Path: path, Service: c.Service, Entries: entries}, nil
}

// StatCopyPath says whether path of the copy is a folder or a file, and its
// size and times. The root is a folder.
func (s *Service) StatCopyPath(ctx context.Context, appID, copyID, path string) (CopyPath, error) {
	c, path, err := s.filesCopy(ctx, appID, copyID, path)
	if err != nil {
		return CopyPath{}, err
	}
	if path == "" {
		return CopyPath{Service: c.Service, Info: FileInfo{Dir: true, Mode: "drwxr-xr-x"}}, nil
	}
	info, err := s.deployer.StatFile(ctx, c.ID, path)
	if err != nil {
		return CopyPath{}, copyFilesError(err)
	}
	return CopyPath{Service: c.Service, Info: info}, nil
}

// CopyFileText reads the file at path for the console to show: its text when it
// is at most MaxViewBytes and valid UTF-8 without a NUL byte, otherwise a reason
// (a file over the limit isn't read at all). A folder is a field error on
// "path". Reading a file is noted in the audit log, with the path and never the
// content.
func (s *Service) CopyFileText(ctx context.Context, appID, copyID, path string) (CopyFileView, error) {
	c, path, err := s.filesCopy(ctx, appID, copyID, path)
	if err != nil {
		return CopyFileView{}, err
	}
	if path == "" { // the root is a folder
		return CopyFileView{}, copyFilesError(deploy.ErrNotAFile)
	}
	info, err := s.deployer.StatFile(ctx, c.ID, path)
	if err != nil {
		return CopyFileView{}, copyFilesError(err)
	}
	if err := readable(info); err != nil {
		return CopyFileView{}, err
	}
	view := CopyFileView{Service: c.Service, Info: info}
	if info.Size > MaxViewBytes {
		view.Reason = ViewTooLarge
		return view, nil
	}
	var buf limitedBuffer
	buf.limit = MaxViewBytes
	if err := s.deployer.ReadFile(ctx, c.ID, path, info.Size, &buf); err != nil {
		return CopyFileView{}, copyFilesError(err)
	}
	s.noteFileRead(ctx, appID, c.ID, info.Path, "view")
	text := buf.b
	if !utf8.Valid(text) || slices.Contains(text, 0) {
		view.Reason = ViewNotText
		return view, nil
	}
	view.Text, view.Shown = string(text), true
	return view, nil
}

// limitedBuffer is a buffer that refuses what is past limit, so a file that
// grew after its size was read can't fill the memory.
type limitedBuffer struct {
	b     []byte
	limit int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if len(l.b)+len(p) > l.limit {
		return 0, errors.New("the file is larger than the limit")
	}
	l.b = append(l.b, p...)
	return len(p), nil
}

// OpenCopyFile checks the file at path of the copy for download and returns it;
// the content is read by WriteTo. A folder is a field error on "path". The
// download is noted in the audit log when it is opened, with the path and never
// the content.
func (s *Service) OpenCopyFile(ctx context.Context, appID, copyID, path string) (*CopyFile, error) {
	c, path, err := s.filesCopy(ctx, appID, copyID, path)
	if err != nil {
		return nil, err
	}
	if path == "" { // the root is a folder
		return nil, copyFilesError(deploy.ErrNotAFile)
	}
	info, err := s.deployer.StatFile(ctx, c.ID, path)
	if err != nil {
		return nil, copyFilesError(err)
	}
	if err := readable(info); err != nil {
		return nil, err
	}
	s.noteFileRead(ctx, appID, c.ID, info.Path, "download")
	return &CopyFile{Service: c.Service, Info: info, svc: s, copyID: c.ID, ctx: ctx}, nil
}

// readable refuses, as a field error on "path", an entry that isn't a regular
// file: a folder, a pipe, a socket or a device.
func readable(info FileInfo) error {
	switch info.Kind() {
	case deploy.KindFile:
		return nil
	case deploy.KindFolder:
		return copyFilesError(deploy.ErrNotAFile)
	}
	return copyFilesError(deploy.ErrSpecialFile)
}

// noteFileRead records that a file of a copy was viewed or downloaded. A GET has
// no audit scope to hold the note back, so it is written at once.
func (s *Service) noteFileRead(ctx context.Context, appID, copyID, path, as string) {
	s.noteApp(ctx, audit.ActionReadFile, appID, nil,
		audit.Change{Path: "copy", Op: audit.OpAdded, To: copyID},
		audit.Change{Path: "path", Op: audit.OpAdded, To: path},
		audit.Change{Path: "as", Op: audit.OpAdded, To: as})
}
