package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The files of a copy (feature 2g4z): Nomad's allocation file system API lists
// a copy's directory and streams its files. The paths here are relative to the
// allocation directory and written without a leading slash: "" is the root,
// "alloc/data", "web/local/caravana-files/0". The directory holds `alloc`
// (shared by the copy's tasks: data, logs, tmp) and one folder per task (local,
// tmp, and the two Nomad refuses to show, secrets and private). With the docker
// driver the container's own file system isn't in it.
//
// Nothing here logs a file's name or content, and no error holds a body: what
// Nomad answers for a read may be the app's own content.

// Errors of the file calls the service maps to answers.
var (
	// ErrPathNotAllowed is a path inside a task's secrets or private folder,
	// which are never shown (and which Nomad refuses to read as well).
	ErrPathNotAllowed = errors.New("a task's secrets and private folders are never shown")
	// ErrFileNotFound is a path that isn't in the copy's directory.
	ErrFileNotFound = errors.New("file not found")
	// ErrNotAFolder is a list of a path that is a file.
	ErrNotAFolder = errors.New("it is a file, not a folder")
	// ErrNotAFile is a read of a path that is a folder.
	ErrNotAFile = errors.New("it is a folder, not a file")
	// ErrSpecialFile is a read of a pipe, a socket or a device, which is never
	// opened: a reader of logmon's pipes in alloc/logs would take the app's
	// output.
	ErrSpecialFile = errors.New("it is a pipe, a socket or a device, which is never opened")
	// ErrPathIsLink is a path through a symbolic link. Links are never followed:
	// Nomad follows a relative one and checks the secrets folder against the path
	// as written, so an app's local/peek -> ../secrets/x would be read (lab,
	// 2026-11-28).
	ErrPathIsLink = errors.New("it goes through a link, and links aren't followed")
)

// FilePathError is a path that can't be one: Reason says why in plain words.
type FilePathError struct{ Reason string }

func (e *FilePathError) Error() string { return e.Reason }

const (
	// maxFilePathBytes is the longest path a call takes.
	maxFilePathBytes = 4096
	// fileChunk is how much of a file one readat call asks for.
	fileChunk = 1 << 20
)

// FileInfo is one entry of a copy's directory, as its folder lists it: a link
// is the link itself, never what it points to.
type FileInfo struct {
	Name    string
	Path    string // relative to the copy's directory, what the other calls take
	Dir     bool
	Size    int64
	Mode    string // as Nomad gives it, such as "-rw-r--r--"; "L" first for a link
	ModTime time.Time
}

// The kinds of entry FileInfo.Kind gives.
const (
	KindFolder = "folder"
	KindFile   = "file"  // a regular file, the only kind that is read
	KindLink   = "link"  // a symbolic link, never followed
	KindOther  = "other" // a pipe, a socket or a device, never opened
)

// Kind says what the entry is, from Nomad's mode: a folder, a regular file, a
// link, or something else.
func (f FileInfo) Kind() string {
	switch {
	case f.Dir:
		return KindFolder
	case strings.HasPrefix(f.Mode, "L"):
		return KindLink
	case f.Mode == "" || strings.HasPrefix(f.Mode, "-"):
		return KindFile
	}
	return KindOther
}

// CleanFilePath checks a path of a copy's directory and returns it written the
// way the other calls take it: no leading or trailing slash, "" for the root.
// A path with a NUL byte or a backslash, an empty segment, a "." or ".."
// segment, or more than 4096 bytes is a *FilePathError. A path inside a task's
// secrets or private folder (second segment, compared without regard to case,
// of a first segment other than alloc) is ErrPathNotAllowed.
func CleanFilePath(p string) (string, error) {
	p = strings.Trim(p, "/")
	switch {
	case len(p) > maxFilePathBytes:
		return "", &FilePathError{Reason: fmt.Sprintf("must be at most %d bytes", maxFilePathBytes)}
	case strings.IndexByte(p, 0) >= 0:
		return "", &FilePathError{Reason: "must not contain a NUL byte"}
	case strings.Contains(p, "\\"):
		return "", &FilePathError{Reason: "must not contain a backslash"}
	case p == "":
		return "", nil
	}
	segs := strings.Split(p, "/")
	for _, s := range segs {
		switch s {
		case "":
			return "", &FilePathError{Reason: "must not have empty parts between slashes"}
		case ".", "..":
			return "", &FilePathError{Reason: "must not contain . or .. parts"}
		}
	}
	if segs[0] != "alloc" && len(segs) >= 2 {
		if second := strings.ToLower(segs[1]); second == "secrets" || second == "private" {
			return "", ErrPathNotAllowed
		}
	}
	return p, nil
}

// fsPath is a clean path as Nomad's file calls take it.
func fsPath(p string) string { return "/" + p }

// ListFiles lists the folder at path of the copy: folders first, then files,
// each by name. In a task's own folder the secrets and private entries are left
// out. A path that is a file is ErrNotAFolder, one that isn't there
// ErrFileNotFound, a path in a secrets or private folder ErrPathNotAllowed, a
// copy Nomad doesn't have ErrCopyNotFound, and a token without read-fs is
// ErrNomadUnavailable naming the capability.
func (d *Deployer) ListFiles(ctx context.Context, copyID, path string) ([]FileInfo, error) {
	path, err := CleanFilePath(path)
	if err != nil {
		return nil, err
	}
	alloc, err := d.logAlloc(ctx, copyID)
	if err != nil {
		return nil, err
	}
	action := "list the files of copy " + copyID
	q := (&api.QueryOptions{}).WithContext(ctx)
	if path != "" { // the root is a folder by definition
		info, err := d.entryAt(ctx, alloc, copyID, path, action)
		if err != nil {
			return nil, err
		}
		switch info.Kind() {
		case KindFolder:
		case KindLink:
			return nil, fmt.Errorf("%w: %s", ErrPathIsLink, path)
		default:
			return nil, fmt.Errorf("%w: %s", ErrNotAFolder, path)
		}
	}
	infos, _, err := d.nomad.AllocFS().List(alloc, fsPath(path), q)
	if err != nil {
		return nil, d.fileError(ctx, action, copyID, err)
	}
	inTask := false
	if segs := strings.Split(path, "/"); path != "" && segs[0] != "alloc" && len(segs) == 1 {
		inTask = true
	}
	files := make([]FileInfo, 0, len(infos))
	for _, fi := range infos {
		if fi == nil || fi.Name == "" {
			continue
		}
		if inTask {
			if n := strings.ToLower(fi.Name); n == "secrets" || n == "private" {
				continue
			}
		}
		files = append(files, fileInfoOf(path, fi))
	}
	slices.SortFunc(files, func(a, b FileInfo) int {
		if a.Dir != b.Dir {
			if a.Dir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return files, nil
}

// StatFile is what Nomad knows of the file or folder at path, with the errors
// of ListFiles. The root has no information of its own and is ErrFileNotFound
// here: list it instead.
func (d *Deployer) StatFile(ctx context.Context, copyID, path string) (FileInfo, error) {
	path, err := CleanFilePath(path)
	if err != nil {
		return FileInfo{}, err
	}
	if path == "" {
		return FileInfo{}, fmt.Errorf("%w: the root has no file information", ErrFileNotFound)
	}
	alloc, err := d.logAlloc(ctx, copyID)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := d.entryAt(ctx, alloc, copyID, path, "read the file information of copy "+copyID)
	if err != nil {
		return FileInfo{}, err
	}
	if info.Kind() == KindLink {
		return FileInfo{}, fmt.Errorf("%w: %s", ErrPathIsLink, path)
	}
	return info, nil
}

// entryAt finds the entry at path (not the root) by listing each folder from
// the root down, as Nomad lists them, without following anything: a part of
// the path that is a link is ErrPathIsLink (the last part too: the caller gets
// it as a link and refuses it), a part that isn't listed ErrFileNotFound, and
// a part that is a file before the end ErrNotAFolder.
//
// Nomad's stat and readat follow links, so this walk is what keeps a link an app
// wrote (local/peek -> ../secrets/x) from reaching a secrets folder. An app
// that swaps a file for a link between the walk and the read could still win
// the race; it gives the reader nothing a terminal into the same copy (the same
// maintain role) doesn't.
func (d *Deployer) entryAt(ctx context.Context, alloc *api.Allocation, copyID, path, action string) (FileInfo, error) {
	q := (&api.QueryOptions{}).WithContext(ctx)
	segs := strings.Split(path, "/")
	dir := ""
	var entry FileInfo
	for i, name := range segs {
		infos, _, err := d.nomad.AllocFS().List(alloc, fsPath(dir), q)
		if err != nil {
			return FileInfo{}, d.fileError(ctx, action, copyID, err)
		}
		found := false
		for _, fi := range infos {
			if fi != nil && fi.Name == name {
				entry, found = fileInfoOf(dir, fi), true
				break
			}
		}
		if !found {
			return FileInfo{}, fmt.Errorf("%w: %s", ErrFileNotFound, path)
		}
		if i == len(segs)-1 {
			break
		}
		switch entry.Kind() {
		case KindFolder:
		case KindLink:
			return FileInfo{}, fmt.Errorf("%w: %s", ErrPathIsLink, path)
		default:
			return FileInfo{}, fmt.Errorf("%w: %s", ErrNotAFolder, path)
		}
		dir = entry.Path
	}
	return entry, nil
}

// fileInfoOf makes a FileInfo of one of Nomad's entries of the folder dir.
func fileInfoOf(dir string, fi *api.AllocFileInfo) FileInfo {
	p := fi.Name
	if dir != "" {
		p = dir + "/" + fi.Name
	}
	return FileInfo{Name: fi.Name, Path: p, Dir: fi.IsDir, Size: fi.Size, Mode: fi.FileMode, ModTime: fi.ModTime}
}

// ReadFile writes the file at path to w, exactly size bytes (the size StatFile
// gave), reading it from Nomad in chunks of 1 MiB. A folder is ErrNotAFile and
// the other errors are those of ListFiles. Like ReadLogAt it knows that Nomad
// answers a read it can't do (a file that vanished, a secret file) as the
// message in the body of a 200: every chunk must be the length asked for, and
// anything else is taken for the message and never quoted, since it may be the
// file's own content. Nothing has been written to w when the first chunk fails.
func (d *Deployer) ReadFile(ctx context.Context, copyID, path string, size int64, w io.Writer) error {
	info, err := d.StatFile(ctx, copyID, path)
	if err != nil {
		return err
	}
	switch info.Kind() {
	case KindFile:
	case KindFolder:
		return fmt.Errorf("%w: %s", ErrNotAFile, info.Path)
	default:
		return fmt.Errorf("%w: %s", ErrSpecialFile, info.Path)
	}
	alloc, err := d.logAlloc(ctx, copyID)
	if err != nil {
		return err
	}
	action := "read a file of copy " + copyID
	q := (&api.QueryOptions{}).WithContext(ctx)
	for off := int64(0); off < size; {
		want := min(fileChunk, size-off)
		r, err := d.nomad.AllocFS().ReadAt(alloc, fsPath(info.Path), off, want, q)
		if err != nil {
			return d.fileError(ctx, action, copyID, err)
		}
		body, err := io.ReadAll(io.LimitReader(r, max(want+1, maxFSMessage)))
		_ = r.Close()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return d.callError(action, err)
		}
		if int64(len(body)) != want {
			return d.fileMessageError(action, copyID, string(body))
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
		off += want
	}
	return nil
}

// fileError maps a failed file call to the errors above.
func (d *Deployer) fileError(ctx context.Context, action, copyID string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	body := errorBody(err)
	switch {
	case strings.Contains(body, "Reading secret file prohibited"), strings.Contains(body, "escapes the alloc directory"):
		return ErrPathNotAllowed
	case statusOf(err) == http.StatusNotFound:
		if strings.Contains(body, "Unknown allocation") {
			return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
		}
		return ErrFileNotFound
	}
	return d.capabilityError(action, "read-fs", err)
}

// fileMessageError maps the text Nomad's client gave instead of a file.
func (d *Deployer) fileMessageError(action, copyID, msg string) error {
	switch {
	case strings.Contains(msg, "Reading secret file prohibited"), strings.Contains(msg, "escapes the alloc directory"):
		return ErrPathNotAllowed
	case strings.Contains(msg, "Unknown allocation"):
		return fmt.Errorf("%w: %s", ErrCopyNotFound, copyID)
	case strings.Contains(msg, "is a directory"):
		return ErrNotAFile
	case strings.Contains(msg, "no such file"):
		return ErrFileNotFound
	case strings.Contains(msg, "Permission denied"):
		return fmt.Errorf("%w: Nomad refused the token (%s); its policy needs the read-fs capability: run the installer again to update it",
			ErrNomadUnavailable, action)
	}
	// Not quoted: a body of the wrong length may be the file's own content.
	return fmt.Errorf("%w: %s: Nomad answered %d bytes that aren't the file", ErrNomadUnavailable, action, len(msg))
}
