package deploytest

import (
	"fmt"
	"net/http"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/nomad/api"
)

// The fake file system of a copy's output (feature 024): the rotated files
// Nomad's logmon keeps in alloc/logs, <task>.<stdout|stderr>.<n>, served the
// way a Nomad client serves GET /v1/client/fs/ls, /stat and /readat. A test
// fills it with AddLogFile and AppendLogLine, takes a file away with
// PurgeLogFile, and the copy's task is its task group, as it is in Caravana.
//
// It is apart from WriteLog's single file, which only the frame stream of
// GET /v1/client/fs/logs reads.
//
// readat is faked as the lab showed Nomad answers it (research R8): an error
// (an unknown copy, a file that isn't there, a token without read-fs) is the
// message as the BODY of a 200, not a status, and an offset at or past the
// end of a file is an empty 200. ls and stat answer real statuses (404, 403).

// logDir is the directory the files are in.
const logDir = "alloc/logs"

// logFile is one rotated file.
type logFile struct {
	data []byte
	mod  time.Time
}

// FileRequest is one request the fake answered for the file system API.
type FileRequest struct {
	Op     string // "ls", "stat" or "readat"
	Copy   string
	Path   string
	Offset int64 // readat only
	Limit  int64 // readat only
}

// AddLogFile puts a file of lines (each ends with a newline) as
// alloc/logs/<task>.<stream>.<index>, replacing the one with that index.
// stream is "stdout" or "stderr". The copy need not exist yet; reading it
// needs the copy. modTime is the file's modification time.
func (n *Nomad) AddLogFile(copyID, stream string, index int, lines []string, modTime time.Time) {
	var data []byte
	for _, l := range lines {
		data = append(data, l...)
		data = append(data, '\n')
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	k := logKey{copyID, stream}
	if n.logFS == nil {
		n.logFS = map[logKey]map[int]*logFile{}
	}
	if n.logFS[k] == nil {
		n.logFS[k] = map[int]*logFile{}
	}
	n.logFS[k][index] = &logFile{data: data, mod: modTime}
}

// AppendLogLine adds line, and its newline, to the end of the newest file of
// the copy's stream (the highest index), as the task writing more output does,
// and moves its modification time to now. With no file yet it makes index 0.
func (n *Nomad) AppendLogLine(copyID, stream, line string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k := logKey{copyID, stream}
	if n.logFS == nil {
		n.logFS = map[logKey]map[int]*logFile{}
	}
	if n.logFS[k] == nil {
		n.logFS[k] = map[int]*logFile{0: {}}
	}
	newest := 0
	for i := range n.logFS[k] {
		newest = max(newest, i)
	}
	f := n.logFS[k][newest]
	f.data = append(f.data, line...)
	f.data = append(f.data, '\n')
	f.mod = time.Now()
}

// PurgeLogFile removes the file, as Nomad does when logmon rotates it away.
// Its later reads are "no such file".
func (n *Nomad) PurgeLogFile(copyID, stream string, index int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.logFS[logKey{copyID, stream}], index)
}

// LogFileSize is the size in bytes of the file, and whether there is one.
func (n *Nomad) LogFileSize(copyID, stream string, index int) (int64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	f, ok := n.logFS[logKey{copyID, stream}][index]
	if !ok {
		return 0, false
	}
	return int64(len(f.data)), true
}

// FileRequests returns the ls, stat and readat requests answered so far,
// oldest first.
func (n *Nomad) FileRequests() []FileRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.fileRequests)
}

// logPath is the path of a log file, as Nomad names it.
func logPath(task, stream string, index int) string {
	return fmt.Sprintf("%s/%s.%s.%d", logDir, task, stream, index)
}

// fakeFileMod is the modification time of the files WriteAllocFile makes.
var fakeFileMod = time.Date(2026, 11, 28, 9, 30, 0, 0, time.UTC)

// fsFile is one file of the copy's directory.
type fsFile struct {
	data []byte
	mod  time.Time
}

// fsFiles is every file the copy has, by path: the rotated log files (named
// after the copy's group, as its one task is) and the files WriteAllocFile put.
// n.mu must be held.
func (n *Nomad) fsFiles(copyID, task string) map[string]fsFile {
	out := map[string]fsFile{}
	for k, files := range n.logFS {
		if k.copyID != copyID {
			continue
		}
		for i, f := range files {
			out[logPath(task, k.stream, i)] = fsFile{data: f.data, mod: f.mod}
		}
	}
	for p, text := range n.files[copyID] {
		out[pathpkg.Clean(p)] = fsFile{data: []byte(text), mod: fakeFileMod}
	}
	return out
}

// fsDirs is every directory the copy has: the ones Nomad makes (the root, alloc
// with data, logs and tmp, and a folder per task with local, tmp, secrets and
// private; the copy's own task and the names sidecar) and every parent of a
// file.
func fsDirs(task string, files map[string]fsFile) map[string]bool {
	dirs := map[string]bool{"": true, "alloc": true, "alloc/data": true, "alloc/logs": true, "alloc/tmp": true}
	for _, t := range []string{task, "names"} {
		dirs[t] = true
		for _, d := range []string{"local", "tmp", "secrets", "private"} {
			dirs[t+"/"+d] = true
		}
	}
	for p := range files {
		for d, _ := splitPath(p); d != ""; d, _ = splitPath(d) {
			dirs[d] = true
		}
	}
	return dirs
}

// escapesMessage is what Nomad's client says of a path a link takes out of
// the allocation directory.
const escapesMessage = "path escapes the alloc directory"

// resolveLinks follows the copy's links in p the way Nomad's client does (lab,
// 2026-11-28): a relative target is followed, an absolute one or one that
// climbs out escapes (ok false). n.mu must be held.
func (n *Nomad) resolveLinks(copyID, p string) (string, bool) {
	links := n.links[copyID]
	for range 16 {
		segs := strings.Split(p, "/")
		moved := false
		for i := range segs {
			prefix := strings.Join(segs[:i+1], "/")
			target, ok := links[prefix]
			if !ok {
				continue
			}
			if strings.HasPrefix(target, "/") {
				return "", false
			}
			parent, _ := splitPath(prefix)
			joined := pathpkg.Join(parent, target)
			if joined == ".." || strings.HasPrefix(joined, "../") {
				return "", false
			}
			if joined == "." {
				joined = ""
			}
			rest := strings.Join(segs[i+1:], "/")
			p = strings.Trim(joined+"/"+rest, "/")
			moved = true
			break
		}
		if !moved {
			return p, true
		}
	}
	return "", false
}

// splitPath splits a clean path into its folder and last part.
func splitPath(p string) (dir, name string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// cleanFSPath resolves the path of a request the way Nomad's client does: as an
// absolute path, so ".." can't climb out and "/" is the root. The result has no
// leading slash.
func cleanFSPath(raw string) string {
	return strings.TrimPrefix(pathpkg.Clean("/"+raw), "/")
}

// prohibited says whether the path is inside a task's secrets or private
// folder, which Nomad's client never lets be read.
func prohibited(p string) bool {
	segs := strings.Split(p, "/")
	if segs[0] == "alloc" || len(segs) < 2 {
		return false
	}
	return segs[1] == "secrets" || segs[1] == "private"
}

// prohibitedMessage is the text Nomad's client gives for such a path.
func prohibitedMessage(p string) string {
	segs := strings.Split(p, "/")
	return "Reading secret file prohibited: /" + segs[0] + "/" + segs[1]
}

// fileInfo is what ls and stat answer for one file.
func fileInfo(name string, size int64, mod time.Time, mode string) *api.AllocFileInfo {
	return &api.AllocFileInfo{Name: name, Size: size, FileMode: mode, ModTime: mod}
}

func dirInfo(name string) *api.AllocFileInfo {
	return &api.AllocFileInfo{Name: name, IsDir: true, FileMode: "drwxr-xr-x", ModTime: fakeFileMod}
}

// serveLs answers GET /v1/client/fs/ls/{id}?path=: the entries of a directory,
// from the files the copy has (WriteAllocFile and the log files) and the
// folders Nomad makes. alloc/logs also has the hidden FIFOs logmon keeps and a
// sidecar's files, as a real one does. Like the lab's Nomad: 404 for a copy or
// directory that isn't there and 500 "Reading secret file prohibited" for a
// task's secrets and private folders.
func (n *Nomad) serveLs(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	raw := strings.Trim(r.URL.Query().Get("path"), "/")
	n.fileRequests = append(n.fileRequests, FileRequest{Op: "ls", Copy: id, Path: raw})
	stub := n.copyStub(id)
	if stub == nil {
		http.Error(w, fmt.Sprintf("Unknown allocation %q", id), http.StatusNotFound)
		return
	}
	path := cleanFSPath(raw)
	if prohibited(path) {
		http.Error(w, prohibitedMessage(path), http.StatusInternalServerError)
		return
	}
	path, ok := n.resolveLinks(id, path)
	if !ok {
		http.Error(w, escapesMessage, http.StatusInternalServerError)
		return
	}
	task := stub.TaskGroup
	files := n.fsFiles(id, task)
	dirs := fsDirs(task, files)
	for l := range n.links[id] {
		for d, _ := splitPath(l); d != ""; d, _ = splitPath(d) {
			dirs[d] = true
		}
	}
	if _, isFile := files[path]; isFile {
		http.Error(w, "readdirent /"+path+": not a directory", http.StatusInternalServerError)
		return
	}
	if !dirs[path] {
		http.Error(w, "open "+path+": no such file or directory", http.StatusNotFound)
		return
	}
	out := []*api.AllocFileInfo{}
	for d := range dirs {
		if dir, name := splitPath(d); d != "" && dir == path {
			out = append(out, dirInfo(name))
		}
	}
	for p, f := range files {
		if dir, name := splitPath(p); dir == path {
			out = append(out, fileInfo(name, int64(len(f.data)), f.mod, "-rw-r--r--"))
		}
	}
	for l, target := range n.links[id] {
		if dir, name := splitPath(l); dir == path {
			out = append(out, fileInfo(name, int64(len(target)), fakeFileMod, "Lrwxrwxrwx"))
		}
	}
	if path == logDir {
		for k := range n.logFS {
			if k.copyID != id {
				continue
			}
			out = append(out, fileInfo("."+task+"."+k.stream+".fifo", 0, time.Now(), "prw-------"))
			out = append(out, fileInfo("names."+k.stream+".0", 0, time.Now(), "-rw-r--r--"))
		}
	}
	slices.SortFunc(out, func(a, b *api.AllocFileInfo) int { return strings.Compare(a.Name, b.Name) })
	n.writeJSON(w, out)
}

// serveStat answers GET /v1/client/fs/stat/{id}?path=: one file's or folder's
// info, 404 when the copy or the path isn't there.
func (n *Nomad) serveStat(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	raw := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	n.fileRequests = append(n.fileRequests, FileRequest{Op: "stat", Copy: id, Path: raw})
	stub := n.copyStub(id)
	if stub == nil {
		http.Error(w, fmt.Sprintf("Unknown allocation %q", id), http.StatusNotFound)
		return
	}
	path := cleanFSPath(raw)
	if prohibited(path) {
		http.Error(w, prohibitedMessage(path), http.StatusInternalServerError)
		return
	}
	_, name := splitPath(path)
	path, ok := n.resolveLinks(id, path)
	if !ok {
		http.Error(w, escapesMessage, http.StatusInternalServerError)
		return
	}
	files := n.fsFiles(id, stub.TaskGroup)
	if f, ok := files[path]; ok {
		info := fileInfo(name, int64(len(f.data)), f.mod, "-rw-r--r--")
		info.ContentType = "text/plain; charset=utf-8"
		n.writeJSON(w, info)
		return
	}
	if fsDirs(stub.TaskGroup, files)[path] {
		n.writeJSON(w, dirInfo(name))
		return
	}
	http.Error(w, "stat /opt/nomad/data/alloc/"+id+"/"+path+": no such file or directory", http.StatusNotFound)
}

// serveReadAt answers GET /v1/client/fs/readat/{id}?path=&offset=&limit=: the
// bytes from offset, at most limit of them. Errors are the message in the body
// of a 200, as the lab's Nomad does it (a missing file, a folder, a task's
// secret file); a read at or past the end is empty.
func (n *Nomad) serveReadAt(w http.ResponseWriter, r *http.Request, id string) {
	q := r.URL.Query()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	req := FileRequest{Op: "readat", Copy: id, Path: strings.TrimPrefix(q.Get("path"), "/")}
	req.Offset, _ = strconv.ParseInt(q.Get("offset"), 10, 64)
	req.Limit, _ = strconv.ParseInt(q.Get("limit"), 10, 64)
	n.fileRequests = append(n.fileRequests, req)

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	stub := n.copyStub(id)
	if stub == nil {
		fmt.Fprintf(w, "Unknown allocation %q", id)
		return
	}
	path := cleanFSPath(req.Path)
	if prohibited(path) {
		fmt.Fprint(w, prohibitedMessage(path))
		return
	}
	path, ok := n.resolveLinks(id, path)
	if !ok {
		fmt.Fprint(w, escapesMessage)
		return
	}
	files := n.fsFiles(id, stub.TaskGroup)
	f, ok := files[path]
	if !ok {
		if fsDirs(stub.TaskGroup, files)[path] {
			fmt.Fprintf(w, "file %q is a directory", "/"+path)
			return
		}
		fmt.Fprintf(w, "stat /opt/nomad/data/alloc/%s/%s: no such file or directory", id, path)
		return
	}
	size := int64(len(f.data))
	if req.Offset < 0 || req.Offset >= size || req.Limit <= 0 {
		return // empty
	}
	end := min(size, req.Offset+req.Limit)
	_, _ = w.Write(f.data[req.Offset:end])
}
