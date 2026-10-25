package deploytest

import (
	"fmt"
	"net/http"
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

// findLogFile returns the file at path of the copy, or nil. n.mu must be held.
func (n *Nomad) findLogFile(copyID, task, path string) *logFile {
	for k, files := range n.logFS {
		if k.copyID != copyID {
			continue
		}
		for i, f := range files {
			if logPath(task, k.stream, i) == path {
				return f
			}
		}
	}
	return nil
}

// fileInfo is what ls and stat answer for one file.
func fileInfo(name string, size int64, mod time.Time, mode string) *api.AllocFileInfo {
	return &api.AllocFileInfo{Name: name, Size: size, FileMode: mode, ModTime: mod}
}

// serveLs answers GET /v1/client/fs/ls/{id}?path=: for alloc/logs the files,
// the hidden FIFOs logmon keeps beside them and a sidecar's files, as a real
// directory has. 404 for a copy or directory that isn't there.
func (n *Nomad) serveLs(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	path := strings.Trim(r.URL.Query().Get("path"), "/")
	n.fileRequests = append(n.fileRequests, FileRequest{Op: "ls", Copy: id, Path: path})
	stub := n.copyStub(id)
	if stub == nil {
		http.Error(w, fmt.Sprintf("Unknown allocation %q", id), http.StatusNotFound)
		return
	}
	if path != logDir {
		http.Error(w, "open "+path+": no such file or directory", http.StatusNotFound)
		return
	}
	task := stub.TaskGroup
	out := []*api.AllocFileInfo{}
	for k, files := range n.logFS {
		if k.copyID != id {
			continue
		}
		out = append(out, fileInfo("."+task+"."+k.stream+".fifo", 0, time.Now(), "prw-------"))
		out = append(out, fileInfo("names."+k.stream+".0", 0, time.Now(), "-rw-r--r--"))
		for i, f := range files {
			out = append(out, fileInfo(task+"."+k.stream+"."+strconv.Itoa(i), int64(len(f.data)), f.mod, "-rw-r--r--"))
		}
	}
	slices.SortFunc(out, func(a, b *api.AllocFileInfo) int { return strings.Compare(a.Name, b.Name) })
	n.writeJSON(w, out)
}

// serveStat answers GET /v1/client/fs/stat/{id}?path=: one file's info, 404
// when the copy or the file isn't there.
func (n *Nomad) serveStat(w http.ResponseWriter, r *http.Request, id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fsStatus != 0 {
		http.Error(w, "Permission denied", n.fsStatus)
		return
	}
	path := strings.TrimPrefix(r.URL.Query().Get("path"), "/")
	n.fileRequests = append(n.fileRequests, FileRequest{Op: "stat", Copy: id, Path: path})
	stub := n.copyStub(id)
	if stub == nil {
		http.Error(w, fmt.Sprintf("Unknown allocation %q", id), http.StatusNotFound)
		return
	}
	f := n.findLogFile(id, stub.TaskGroup, path)
	if f == nil {
		http.Error(w, "stat /opt/nomad/data/alloc/"+id+"/"+path+": no such file or directory", http.StatusNotFound)
		return
	}
	name := path[strings.LastIndex(path, "/")+1:]
	info := fileInfo(name, int64(len(f.data)), f.mod, "-rw-r--r--")
	info.ContentType = "text/plain; charset=utf-8"
	n.writeJSON(w, info)
}

// serveReadAt answers GET /v1/client/fs/readat/{id}?path=&offset=&limit=: the
// bytes from offset, at most limit of them. Errors are the message in the body
// of a 200, as the lab's Nomad does it; a read at or past the end is empty.
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
	f := n.findLogFile(id, stub.TaskGroup, req.Path)
	if f == nil {
		fmt.Fprintf(w, "stat /opt/nomad/data/alloc/%s/%s: no such file or directory", id, req.Path)
		return
	}
	size := int64(len(f.data))
	if req.Offset < 0 || req.Offset >= size || req.Limit <= 0 {
		return // empty
	}
	end := min(size, req.Offset+req.Limit)
	_, _ = w.Write(f.data[req.Offset:end])
}
